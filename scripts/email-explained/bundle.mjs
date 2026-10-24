// Builds Email, explained (docs/email-explained) into the content bundle that
// rest-mail.com renders: one JSON file holding every chapter, split into its
// sections as HTML, and every diagram rendered by Archify beside it.
//
//   node bundle.mjs --archify <path to archify's bin/archify.mjs> [--out <dir>] [--version <x.y.z>]
//
// CI builds it on every change to main and publishes it as
// ghcr.io/rest-mail/email-explained:<version> (email-explained.yml), which the
// website picks up by itself. The version's major is FORMAT, so a website that
// reads one format never takes a bundle of another.
//
// The Markdown stays the source of truth, readable on GitHub as it is. This
// only reads it, following the conventions in
// docs/design/email-explained-website.md:
//
// - front matter gives a chapter its title, part, summary and diagram;
// - each `##` heading starts a section, with GitHub's anchor as its id;
// - an image of diagrams/<name>.svg, beside its <name>.<type>.json source,
//   becomes a placeholder the website fills with the interactive diagram;
// - links between chapters become site paths, and links to code become
//   GitHub links pinned to the commit the bundle was built from.
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { copyFile, mkdir, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { basename, dirname, join, posix, relative, resolve } from "node:path";
import { parseArgs } from "node:util";
import { Marked } from "marked";

export const FORMAT = 1;
const REPOSITORY = "rest-mail/rest-mail-server";
export const GUIDE = "docs/email-explained";
const SITE_PATH = "/email-explained";
// For the people writing it, not part of the guide.
export const SKIP = new Set(["WRITING.md"]);
// Archify's diagram types: diagrams/<name>.<type>.json is a diagram's source.
// Its other JSON files there are receipts from checking one.
export const TYPES = new Set(["architecture", "workflow", "sequence", "dataflow", "lifecycle", "erd", "tree", "class", "timeline", "waterfall"]);

const git = (...args) => execFileSync("git", args, { encoding: "utf8" }).trim();

/**
 * Front matter: `key: value` lines between two `---` lines, and the rest. A
 * value with a colon in it is written in double quotes, as YAML needs for
 * GitHub to show it.
 */
export function splitFrontMatter(text) {
  const match = text.match(/^---\n([\s\S]*?)\n---\n/);
  if (!match) return { meta: {}, body: text };
  const meta = {};
  for (const line of match[1].split("\n")) {
    const pair = line.match(/^([a-z]+):\s*(.*)$/);
    if (!pair) throw new Error(`front matter line not understood: ${line}`);
    const quoted = pair[2].match(/^"((?:[^"\\]|\\.)*)"$/);
    meta[pair[1]] = quoted ? quoted[1].replace(/\\(.)/g, "$1") : pair[2];
  }
  return { meta, body: text.slice(match[0].length) };
}

/** The anchor GitHub gives a heading, numbered when one repeats. */
export function anchorer() {
  const seen = new Map();
  return (text) => {
    const base = text
      .toLowerCase()
      .replace(/[^\p{L}\p{N}\s_-]/gu, "")
      .replace(/\s/g, "-");
    const count = seen.get(base) ?? 0;
    seen.set(base, count + 1);
    return count === 0 ? base : `${base}-${count}`;
  };
}

/** A chapter's address on the site, from its path under the guide. */
export const slugOf = (file) => (basename(file) === "README.md" ? dirname(file) : file.replace(/\.md$/, "")).replace(/^\.$/, "");

export const plain = (html) => html.replace(/<[^>]+>/g, "").replace(/&amp;/g, "&").replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&quot;/g, '"').replace(/&#39;/g, "'");
const escape = (text) => text.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;");

/**
 * One chapter, as the website wants it. `file` is its path under the guide,
 * `files` every chapter's, and `diagrams` the diagram sources that exist.
 */
export function compileChapter({ file, text, commit, files, diagrams }) {
  const { meta, body } = splitFrontMatter(text);
  const anchor = anchorer();
  const here = posix.join(GUIDE, posix.dirname(file));

  // A link or image path, as the website should use it.
  const target = (href) => {
    if (/^([a-z]+:|#)/i.test(href)) return { href };
    const [path, hash = ""] = href.split("#");
    const resolved = posix.normalize(posix.join(here, path));
    const chapter = posix.relative(GUIDE, resolved);
    if (files.has(chapter)) {
      const slug = slugOf(chapter);
      return { href: `${SITE_PATH}${slug ? `/${slug}` : ""}${hash ? `#${hash}` : ""}`, chapter: slug };
    }
    return { href: `https://github.com/${REPOSITORY}/blob/${commit}/${resolved}${hash ? `#${hash}` : ""}`, resolved };
  };

  const marked = new Marked({
    gfm: true,
    renderer: {
      heading({ tokens, depth }) {
        const html = this.parser.parseInline(tokens);
        return `<h${depth} id="${anchor(plain(html))}">${html}</h${depth}>\n`;
      },
      // A diagram is a figure of its own, not an image inside a paragraph.
      paragraph({ tokens }) {
        const html = this.parser.parseInline(tokens);
        return tokens.length === 1 && html.startsWith("<figure") ? `${html}\n` : `<p>${html}</p>\n`;
      },
      link({ href, title, tokens }) {
        const html = this.parser.parseInline(tokens);
        const titled = title ? ` title="${escape(title)}"` : "";
        return `<a href="${escape(target(href).href)}"${titled}>${html}</a>`;
      },
      image({ href, text }) {
        const { resolved } = target(href);
        const name = resolved?.match(new RegExp(`^${GUIDE}/diagrams/([a-z0-9-]+)\\.svg$`))?.[1];
        if (name && diagrams.has(name)) {
          return `<figure class="guide-diagram" data-diagram="${name}"><img src="diagrams/${name}.svg" alt="${escape(text)}"></figure>`;
        }
        throw new Error(`${file}: only diagram images are supported so far, not ${href}`);
      },
    },
  });

  const tokens = marked.lexer(body);
  // The chapter's own title is its front matter's; the `#` heading repeats it.
  const first = tokens.findIndex((token) => token.type !== "space");
  let title = meta.title;
  if (tokens[first]?.type === "heading" && tokens[first].depth === 1) {
    title ??= tokens[first].text;
    anchor(tokens[first].text);
    tokens.splice(first, 1);
  }
  if (!title) throw new Error(`${file}: no title`);

  // Everything before the first `##` is the introduction; each `##` a section.
  const groups = [{ heading: null, tokens: [] }];
  for (const token of tokens) {
    if (token.type === "heading" && token.depth === 2) groups.push({ heading: token, tokens: [] });
    else groups.at(-1).tokens.push(token);
  }
  const render = (list) => marked.parser(Object.assign(list, { links: tokens.links }));
  const [intro, ...rest] = groups;
  const sections = rest.map(({ heading, tokens: list }) => {
    const headingHtml = render([heading]);
    const [, id, titleHtml] = headingHtml.match(/^<h2 id="([^"]*)">([\s\S]*)<\/h2>\n$/);
    return { id, title: plain(titleHtml), titleHtml, html: render(list) };
  });

  const diagram = meta.diagram ? basename(meta.diagram).replace(/\.[a-z]+\.json$/, "") : null;
  if (diagram && !diagrams.has(diagram)) throw new Error(`${file}: front matter names a missing diagram, ${meta.diagram}`);
  return {
    slug: slugOf(file),
    file: posix.join(GUIDE, file),
    title,
    part: meta.part ?? null,
    summary: meta.summary ?? null,
    diagram,
    intro: render(intro.tokens),
    sections,
  };
}

/**
 * Archify carries its typefaces inside the page, as data: URLs. A site that
 * allows typefaces only from itself refuses those, so each one becomes a file
 * of its own beside the diagram, named for its contents.
 */
export function extractFonts(html) {
  const fonts = new Map();
  const out = html.replace(/url\(data:font\/(woff2?);base64,([A-Za-z0-9+/=]+)\)/g, (_, ext, data) => {
    const bytes = Buffer.from(data, "base64");
    const file = `fonts/${createHash("sha256").update(bytes).digest("hex").slice(0, 16)}.${ext}`;
    fonts.set(file, bytes);
    return `url(${file})`;
  });
  return { html: out, fonts };
}

/**
 * The guide's files in reading order: the front page, then the chapters in
 * the order it links them, then any it does not link, by path.
 */
export function readingOrder(readme, files) {
  const linked = [];
  for (const [, href] of readme.matchAll(/\]\(([^)#\s]+\.md)(?:#[^)]*)?\)/g)) {
    const file = posix.normalize(href);
    if (files.includes(file) && !linked.includes(file)) linked.push(file);
  }
  const front = files.filter((f) => f === "README.md");
  const rest = files.filter((f) => f !== "README.md" && !linked.includes(f)).sort();
  return [...front, ...linked.filter((f) => f !== "README.md"), ...rest];
}

async function markdownFiles(dir, base = dir) {
  const found = [];
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) found.push(...(await markdownFiles(path, base)));
    else if (entry.name.endsWith(".md") && !SKIP.has(entry.name)) found.push(relative(base, path).split("\\").join("/"));
  }
  return found.sort();
}

async function main() {
  const { values } = parseArgs({
    options: { out: { type: "string", default: "build/email-explained" }, archify: { type: "string" }, version: { type: "string" } },
  });
  if (!values.archify) throw new Error("--archify <path to bin/archify.mjs> is required: it renders the diagrams");
  if (values.version && !new RegExp(`^${FORMAT}\\.\\d+\\.\\d+$`).test(values.version)) throw new Error(`--version ${values.version}: a bundle of format ${FORMAT} is version ${FORMAT}.x.y`);
  const root = git("rev-parse", "--show-toplevel");
  const commit = process.env.GITHUB_SHA || git("rev-parse", "HEAD");
  const guide = join(root, GUIDE);
  const out = resolve(root, values.out);

  await rm(out, { recursive: true, force: true });
  await mkdir(join(out, "diagrams"), { recursive: true });

  const diagrams = new Map();
  for (const name of (await readdir(join(guide, "diagrams"))).sort()) {
    const source = name.match(/^([a-z0-9-]+)\.([a-z]+)\.json$/);
    if (!source || !TYPES.has(source[2])) continue;
    const [, diagram, type] = source;
    const html = `diagrams/${diagram}.html`;
    execFileSync(process.execPath, [values.archify, "render", type, join(guide, "diagrams", name), join(out, html)], {
      env: { ...process.env, ARCHIFY_UPDATE_CHECK_DISABLED: "1" },
      stdio: ["ignore", "ignore", "inherit"],
    });
    const { html: page, fonts } = extractFonts(await readFile(join(out, html), "utf8"));
    await writeFile(join(out, html), page);
    await mkdir(join(out, "diagrams", "fonts"), { recursive: true });
    for (const [file, bytes] of fonts) await writeFile(join(out, "diagrams", file), bytes);
    await copyFile(join(guide, "diagrams", `${diagram}.svg`), join(out, "diagrams", `${diagram}.svg`));
    diagrams.set(diagram, { name: diagram, type, source: `${GUIDE}/diagrams/${name}`, html, image: `diagrams/${diagram}.svg` });
  }

  const files = readingOrder(await readFile(join(guide, "README.md"), "utf8"), await markdownFiles(guide));
  const chapters = [];
  for (const file of files) {
    const text = await readFile(join(guide, file), "utf8");
    chapters.push(compileChapter({ file, text, commit, files: new Set(files), diagrams }));
  }

  const bundle = { format: FORMAT, version: values.version ?? null, source: { repository: REPOSITORY, commit, path: GUIDE }, chapters, diagrams: [...diagrams.values()] };
  await writeFile(join(out, "bundle.json"), `${JSON.stringify(bundle, null, 2)}\n`);
  console.log(`${relative(root, out)}: ${values.version ?? "unversioned"}, ${chapters.length} chapters, ${diagrams.size} diagrams, from ${commit.slice(0, 12)}`);
}

if (import.meta.url === `file://${process.argv[1]}`) await main();

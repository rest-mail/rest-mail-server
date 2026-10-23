// Builds Email, explained (docs/email-explained) into the content bundle that
// rest-mail.com renders: one JSON file holding every chapter, split into its
// sections as HTML, and every diagram rendered by Archify beside it.
//
//   node bundle.mjs --archify <path to archify's bin/archify.mjs> [--out <dir>]
//
// The Markdown stays the source of truth, readable on GitHub as it is. This
// only reads it, following the conventions in
// docs/design/email-explained-website.md:
//
// - front matter gives a chapter its title, part, summary and diagram;
// - each `##` heading starts a section, with GitHub's anchor as its id;
// - an image of diagrams/<name>.png, beside its <name>.<type>.json source,
//   becomes a placeholder the website fills with the interactive diagram;
// - links between chapters become site paths, and links to code become
//   GitHub links pinned to the commit the bundle was built from.
import { execFileSync } from "node:child_process";
import { copyFile, mkdir, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { basename, dirname, join, posix, relative, resolve } from "node:path";
import { parseArgs } from "node:util";
import { Marked } from "marked";

export const FORMAT = 1;
const REPOSITORY = "rest-mail/rest-mail-server";
const GUIDE = "docs/email-explained";
const SITE_PATH = "/email-explained";
// For the people writing it, not part of the guide.
const SKIP = new Set(["WRITING.md"]);
// Archify's diagram types: diagrams/<name>.<type>.json is a diagram's source.
// Its other JSON files there are receipts from checking one.
const TYPES = new Set(["architecture", "workflow", "sequence", "dataflow", "lifecycle", "erd", "tree", "class", "timeline", "waterfall"]);

const git = (...args) => execFileSync("git", args, { encoding: "utf8" }).trim();

/** Front matter: `key: value` lines between two `---` lines, and the rest. */
export function splitFrontMatter(text) {
  const match = text.match(/^---\n([\s\S]*?)\n---\n/);
  if (!match) return { meta: {}, body: text };
  const meta = {};
  for (const line of match[1].split("\n")) {
    const pair = line.match(/^([a-z]+):\s*(.*)$/);
    if (!pair) throw new Error(`front matter line not understood: ${line}`);
    meta[pair[1]] = pair[2];
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

const plain = (html) => html.replace(/<[^>]+>/g, "").replace(/&amp;/g, "&").replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&quot;/g, '"').replace(/&#39;/g, "'");
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
        const name = resolved?.match(new RegExp(`^${GUIDE}/diagrams/([a-z0-9-]+)\\.png$`))?.[1];
        if (name && diagrams.has(name)) {
          return `<figure class="guide-diagram" data-diagram="${name}"><img src="diagrams/${name}.png" alt="${escape(text)}"></figure>`;
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
    options: { out: { type: "string", default: "build/email-explained" }, archify: { type: "string" } },
  });
  if (!values.archify) throw new Error("--archify <path to bin/archify.mjs> is required: it renders the diagrams");
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
    await copyFile(join(guide, "diagrams", `${diagram}.png`), join(out, "diagrams", `${diagram}.png`));
    diagrams.set(diagram, { name: diagram, type, source: `${GUIDE}/diagrams/${name}`, html, image: `diagrams/${diagram}.png` });
  }

  const files = await markdownFiles(guide);
  const chapters = [];
  for (const file of files) {
    const text = await readFile(join(guide, file), "utf8");
    chapters.push(compileChapter({ file, text, commit, files: new Set(files), diagrams }));
  }

  const bundle = { format: FORMAT, source: { repository: REPOSITORY, commit, path: GUIDE }, chapters, diagrams: [...diagrams.values()] };
  await writeFile(join(out, "bundle.json"), `${JSON.stringify(bundle, null, 2)}\n`);
  console.log(`${relative(root, out)}: ${chapters.length} chapters, ${diagrams.size} diagrams, from ${commit.slice(0, 12)}`);
}

if (import.meta.url === `file://${process.argv[1]}`) await main();

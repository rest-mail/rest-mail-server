// Checks Email, explained (docs/email-explained) the way CI does, so a broken
// link, an unsourced chapter or an orphaned diagram fails the pull request
// that broke it, as docs/design/email-explained-website.md asks.
//
//   node check.mjs                          the guide's own checks
//   node check.mjs --archify <archify.mjs>  and every diagram source is valid
//   node check.mjs --external               and every outside link still answers
//
// The guide's own checks need nothing but the repository. Outside links change
// on other people's schedules, so CI checks those daily, not on every PR.
import { execFileSync } from "node:child_process";
import { get as httpGet } from "node:http";
import { get as httpsGet } from "node:https";
import { readdir, readFile, stat } from "node:fs/promises";
import { join, posix, relative } from "node:path";
import { parseArgs } from "node:util";
import { Marked } from "marked";
import { anchorer, GUIDE, plain, SKIP, splitFrontMatter, TYPES } from "./bundle.mjs";

// What a chapter's front matter may say. The front page (README.md) has none.
const REQUIRED = ["title", "part", "summary"];
const OPTIONAL = ["diagram", "sources"];
// A page whose claims are sourced in the chapters it links (the glossary, the
// checklist, the summary table) says `sources: linked` instead of having a
// Sources section of its own.
const SOURCES_LINKED = "linked";

const marked = new Marked({ gfm: true });

/** Every link and image in a page: its target, and the line it is on. */
export function linksOf(text) {
  const found = [];
  marked.walkTokens(marked.lexer(text), (token) => {
    if (token.type === "link" || token.type === "image") found.push({ href: token.href, image: token.type === "image", raw: token.raw });
  });
  return found.map((link) => {
    const at = text.indexOf(link.raw);
    return { ...link, line: at < 0 ? null : text.slice(0, at).split("\n").length };
  });
}

/** The anchors GitHub gives a page's headings. */
export function anchorsOf(text) {
  const anchor = anchorer();
  const anchors = new Set();
  marked.walkTokens(marked.lexer(splitFrontMatter(text).body), (token) => {
    if (token.type === "heading") anchors.add(anchor(plain(marked.parseInline(token.text))));
  });
  return anchors;
}

/**
 * The guide's own checks. `pages` maps each Markdown file under the guide to
 * its text; `repo` answers for any path in the repository: whether it is a
 * file or a directory, and a file's text. Returns the problems found, each
 * with the file and line it is on.
 */
export function checkGuide({ pages, repo, diagramFiles }) {
  const problems = [];
  const problem = (file, line, message) => problems.push({ file: posix.join(GUIDE, file), line, message });
  const anchors = new Map([...pages].map(([file, text]) => [file, anchorsOf(text)]));
  const used = new Set();

  for (const [file, text] of pages) {
    const front = file === "README.md";
    const raw = text.match(/^---\n([\s\S]*?)\n---\n/)?.[1];
    let meta = {};
    try {
      meta = splitFrontMatter(text).meta;
    } catch (error) {
      problem(file, 1, error.message);
    }

    // Front matter.
    if (!front) {
      if (!raw) problem(file, 1, "no front matter: a chapter needs title, part and summary");
      for (const key of REQUIRED) if (raw && !meta[key]) problem(file, 1, `front matter has no ${key}`);
      for (const key of Object.keys(meta)) if (![...REQUIRED, ...OPTIONAL].includes(key)) problem(file, 1, `front matter key ${key} is not one the website reads`);
      if (meta.sources && meta.sources !== SOURCES_LINKED) problem(file, 1, `sources: ${meta.sources}: the only value is "${SOURCES_LINKED}"`);
      for (const [i, line] of (raw ?? "").split("\n").entries()) {
        const value = line.match(/^[a-z]+:\s*(.*)$/)?.[1] ?? "";
        if (!value.startsWith('"') && /:\s|^[-?:,[\]{}#&*!|>'%@`]/.test(value)) problem(file, i + 2, "this value needs double quotes for GitHub to read it as YAML");
      }
    }

    // Sources: every chapter has its own, as WRITING.md requires.
    if (!front && meta.sources !== SOURCES_LINKED && !/^## Sources\s*$/m.test(text)) problem(file, null, `no "## Sources" section (or "sources: ${SOURCES_LINKED}" for a page sourced in the chapters it links)`);

    // The diagram its front matter names.
    if (meta.diagram) {
      const path = posix.normalize(posix.join(posix.dirname(file), meta.diagram));
      const name = path.match(/^diagrams\/([a-z0-9-]+)\.([a-z]+)\.json$/);
      if (!name || !TYPES.has(name[2]) || !diagramFiles.has(posix.basename(path))) problem(file, 1, `front matter names a diagram that does not exist: ${meta.diagram}`);
      else used.add(name[1]);
    }

    // Links and images.
    for (const { href, image, line } of linksOf(text)) {
      if (/^(https?:|mailto:)/i.test(href)) continue;
      if (/^[a-z][a-z0-9+.-]*:/i.test(href)) {
        problem(file, line, `link to ${href}: only http, https and mailto links leave the guide`);
        continue;
      }
      const [path, hash] = href.split("#");
      if (!path) {
        if (hash && !anchors.get(file).has(hash)) problem(file, line, `#${hash} is not a heading on this page`);
        continue;
      }
      const inGuide = posix.normalize(posix.join(posix.dirname(file), path));
      const inRepo = posix.normalize(posix.join(GUIDE, inGuide));
      if (inRepo.startsWith("../")) {
        problem(file, line, `${href} leaves the repository`);
        continue;
      }
      if (image) {
        const diagram = inGuide.match(/^diagrams\/([a-z0-9-]+)\.png$/)?.[1];
        if (!diagram || !diagramFiles.has(`${diagram}.png`)) problem(file, line, `image ${href} is not a diagram's static image`);
        else used.add(diagram);
        continue;
      }
      const kind = repo.kind(inRepo.replace(/\/$/, ""));
      if (!kind) {
        problem(file, line, `${href} does not exist`);
        continue;
      }
      if (!hash) continue;
      if (pages.has(inGuide)) {
        if (!anchors.get(inGuide).has(hash)) problem(file, line, `${href}: #${hash} is not a heading there`);
      } else if (/^L\d+(-L\d+)?$/.test(hash) && kind === "file") {
        const last = Math.max(...hash.match(/\d+/g).map(Number));
        const lines = repo.text(inRepo).split("\n").length;
        if (last > lines) problem(file, line, `${href}: line ${last} is past the end (${lines} lines)`);
      } else if (inRepo.endsWith(".md") && kind === "file") {
        if (!anchorsOf(repo.text(inRepo)).has(hash)) problem(file, line, `${href}: #${hash} is not a heading there`);
      }
    }
  }

  // Diagrams: every source is used, and has its static image.
  for (const name of [...diagramFiles].sort()) {
    const source = name.match(/^([a-z0-9-]+)\.([a-z]+)\.json$/);
    if (!source || !TYPES.has(source[2])) continue;
    if (!used.has(source[1])) problem(`diagrams/${name}`, null, "no chapter shows this diagram");
    if (!diagramFiles.has(`${source[1]}.png`)) problem(`diagrams/${name}`, null, `no static image (${source[1]}.png) beside it`);
  }
  return problems;
}

/** Every outside link in the guide, and where each one is used. */
export function outsideLinks(pages) {
  const links = new Map();
  for (const [file, text] of pages) {
    for (const { href, line } of linksOf(text)) {
      if (!/^https?:/i.test(href)) continue;
      if (!links.has(href)) links.set(href, []);
      links.get(href).push(`${posix.join(GUIDE, file)}:${line}`);
    }
  }
  return links;
}

// Pages that build their headings in the browser, so an anchor cannot be seen
// in the HTML a plain request gets.
const SCRIPTED = /^https:\/\/(github\.com|support\.google\.com|learn\.microsoft\.com|techcommunity\.microsoft\.com)\//;

const AGENT = "rest-mail-guide-link-check (+https://github.com/rest-mail/rest-mail-server)";

/**
 * Only the status, from the response's first line, following redirects. For
 * the few servers whose responses fetch() refuses outright: cr.yp.to sends
 * both Content-Length and Transfer-Encoding, which HTTP forbids. Node's lenient
 * parser reads past that, and nothing here reads the body.
 */
function statusOnly(url, hops = 5) {
  return new Promise((resolve, reject) => {
    const request = (url.startsWith("https:") ? httpsGet : httpGet)(url, { headers: { "user-agent": AGENT }, timeout: 30_000, insecureHTTPParser: true }, (response) => {
      response.destroy();
      const next = response.headers.location;
      if (response.statusCode >= 300 && response.statusCode < 400 && next && hops > 0) resolve(statusOnly(new URL(next, url).href, hops - 1));
      else resolve(response.statusCode);
    });
    request.on("timeout", () => request.destroy(new Error("timed out")));
    request.on("error", reject);
  });
}

async function fetchPage(url) {
  for (let attempt = 1; ; attempt++) {
    try {
      const response = await fetch(url, {
        redirect: "follow",
        signal: AbortSignal.timeout(30_000),
        headers: { "user-agent": AGENT, accept: "text/html,*/*" },
      });
      if (response.status >= 500 && attempt < 3) throw new Error(`HTTP ${response.status}`);
      // The status says whether the page is there. A body that cannot be read
      // (one server sends a wrong Content-Length) only means its anchors
      // cannot be checked.
      const html = (response.headers.get("content-type") ?? "").includes("html");
      const body = html ? await response.text().catch(() => "") : "";
      return { status: response.status, body };
    } catch (error) {
      if (attempt >= 3) {
        const status = await statusOnly(url).catch(() => 0);
        return status ? { status, body: "" } : { status: 0, body: "", error: error.message };
      }
      await new Promise((r) => setTimeout(r, 2000 * attempt));
    }
  }
}

/**
 * Asks every outside link once (a page once, however many anchors in it are
 * used) and returns the ones that are broken, and the ones that refused to be
 * checked (401, 403, 429: a site turning away robots, not a missing page).
 */
export async function checkOutside(links, { concurrency = 6 } = {}) {
  const pagesByUrl = new Map();
  for (const href of links.keys()) {
    const url = href.split("#")[0];
    if (!pagesByUrl.has(url)) pagesByUrl.set(url, []);
    pagesByUrl.get(url).push(href);
  }
  const broken = [];
  const refused = [];
  const queue = [...pagesByUrl.keys()];
  const worker = async () => {
    for (let url; (url = queue.shift()); ) {
      const page = await fetchPage(url);
      const where = (href) => links.get(href).join(", ");
      if ([401, 403, 429].includes(page.status)) {
        refused.push({ url, status: page.status, where: pagesByUrl.get(url).map(where).join(", ") });
        continue;
      }
      if (page.status === 0 || page.status >= 400) {
        for (const href of pagesByUrl.get(url)) broken.push({ href, why: page.error ?? `HTTP ${page.status}`, where: where(href) });
        continue;
      }
      if (SCRIPTED.test(url) || !page.body) continue;
      for (const href of pagesByUrl.get(url)) {
        const hash = href.split("#")[1];
        if (!hash) continue;
        const id = decodeURIComponent(hash).replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
        if (!new RegExp(`\\b(id|name)=["']?${id}["'\\s>]`).test(page.body)) broken.push({ href, why: `no #${hash} on the page`, where: where(href) });
      }
    }
  };
  await Promise.all(Array.from({ length: concurrency }, worker));
  return { broken, refused, pages: pagesByUrl.size };
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
  const { values } = parseArgs({ options: { archify: { type: "string" }, external: { type: "boolean", default: false } } });
  const root = execFileSync("git", ["rev-parse", "--show-toplevel"], { encoding: "utf8" }).trim();
  const guide = join(root, GUIDE);
  const pages = new Map();
  for (const file of await markdownFiles(guide)) pages.set(file, await readFile(join(guide, file), "utf8"));
  const kinds = new Map();
  const repo = {
    kind: (path) => kinds.get(path),
    text: (path) => execFileSync("git", ["-C", root, "show", `:${path}`], { encoding: "utf8", maxBuffer: 1 << 26 }),
  };
  // What git tracks: a file, or a directory holding one.
  for (const path of execFileSync("git", ["-C", root, "ls-files", "-z"], { encoding: "utf8", maxBuffer: 1 << 26 }).split("\0").filter(Boolean)) {
    kinds.set(path, "file");
    for (let dir = posix.dirname(path); dir !== "."; dir = posix.dirname(dir)) kinds.set(dir, "directory");
  }
  const diagramFiles = new Set((await readdir(join(guide, "diagrams"))).filter((name) => kinds.has(posix.join(GUIDE, "diagrams", name))));

  const problems = checkGuide({ pages, repo, diagramFiles });

  if (values.archify) {
    for (const name of [...diagramFiles].sort()) {
      const source = name.match(/^([a-z0-9-]+)\.([a-z]+)\.json$/);
      if (!source || !TYPES.has(source[2])) continue;
      try {
        execFileSync(process.execPath, [values.archify, "validate", source[2], join(guide, "diagrams", name), "--json"], {
          env: { ...process.env, ARCHIFY_UPDATE_CHECK_DISABLED: "1" },
          stdio: ["ignore", "pipe", "pipe"],
        });
      } catch (error) {
        const said = `${error.stdout ?? ""}${error.stderr ?? ""}`.trim().split("\n").slice(-5).join(" ");
        problems.push({ file: posix.join(GUIDE, "diagrams", name), line: null, message: `Archify does not accept it: ${said}` });
      }
    }
  }

  for (const { file, line, message } of problems) console.log(`${file}${line ? `:${line}` : ""}: ${message}`);
  console.log(`${pages.size} pages, ${diagramFiles.size} diagram files: ${problems.length} problem${problems.length === 1 ? "" : "s"}`);

  let failed = problems.length > 0;
  if (values.external) {
    const links = outsideLinks(pages);
    const { broken, refused, pages: asked } = await checkOutside(links);
    for (const { url, status, where } of refused) console.log(`refused to be checked (HTTP ${status}): ${url}  (${where})`);
    for (const { href, why, where } of broken) console.log(`broken: ${href}: ${why}  (${where})`);
    console.log(`${links.size} outside links on ${asked} pages: ${broken.length} broken, ${refused.length} refused to be checked`);
    failed ||= broken.length > 0;
  }
  process.exitCode = failed ? 1 : 0;
}

if (import.meta.url === `file://${process.argv[1]}`) await main();

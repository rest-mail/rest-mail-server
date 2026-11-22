// Checks Email, explained (docs/email-explained) the way CI does, so a broken
// link, an unsourced chapter or an orphaned diagram fails the pull request
// that broke it, as docs/design/email-explained-website.md asks.
//
//   node check.mjs                          the guide's own checks
//   node check.mjs --archify <archify.mjs>  and every diagram source is valid
//   node check.mjs --external               and every outside link still answers
//                  [--exceptions <file>]    (default outside-link-exceptions.json)
//                  [--new-since <ref>]      only the outside links added since ref,
//                                           RFC links included: run it when you add links
//
// The guide's own checks need nothing but the repository. Outside links change
// on other people's schedules, so CI checks those daily and when the guide is
// published (scheduled.yml, email-explained.yml), not on every PR. A site that
// refuses to be checked (HTTP 401, 403, 429) passes only through a dated,
// reasoned exception in outside-link-exceptions.json (#2120).
//
// RFC links (www.rfc-editor.org/rfc/...) are checked when they are added, not
// daily: a published RFC never changes, so a link that answered once keeps
// answering, and rfc-editor.org refuses GitHub's runners (HTTP 403, #2199).
// Whoever adds one runs `node check.mjs --external --new-since origin/main`
// from their own machine before the pull request.
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { get as httpGet } from "node:http";
import { get as httpsGet } from "node:https";
import { readdir, readFile, stat } from "node:fs/promises";
import { dirname, join, posix, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";
import { Marked } from "marked";
import { anchorer, GUIDE, plain, SKIP, splitFrontMatter, TYPES } from "./bundle.mjs";
import { lookHash, PRESET } from "./look.mjs";

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
 * file or a directory, and a file's text. `archify` is the pinned version and
 * commit every static image must say drew it, and `look` the hash of the look
 * (look.mjs) it must say it was drawn in. Returns the problems found, each
 * with the file and line it is on.
 */
export function checkGuide({ pages, repo, diagramFiles, archify, look }) {
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
        const diagram = inGuide.match(/^diagrams\/([a-z0-9-]+)\.svg$/)?.[1];
        if (!diagram || !diagramFiles.has(`${diagram}.svg`)) problem(file, line, `image ${href} is not a diagram's static image`);
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

  // Diagrams: every source is used and drawn in the site's look, and has its
  // static image, exported from the source as it is now by the pinned Archify
  // in the current look (svg.mjs records all three).
  for (const name of [...diagramFiles].sort()) {
    const source = name.match(/^([a-z0-9-]+)\.([a-z]+)\.json$/);
    if (!source || !TYPES.has(source[2])) continue;
    if (!used.has(source[1])) problem(`diagrams/${name}`, null, "no chapter shows this diagram");
    let preset;
    try {
      preset = JSON.parse(repo.text(posix.join(GUIDE, "diagrams", name)) ?? "").meta?.visual_preset;
    } catch {
      preset = undefined;
    }
    if (preset !== PRESET) problem(`diagrams/${name}`, null, `is not drawn in the site's look: its meta.visual_preset must be "${PRESET}" (look.mjs)`);
    const image = `${source[1]}.svg`;
    if (!diagramFiles.has(image)) {
      problem(`diagrams/${name}`, null, `no static image (${image}) beside it`);
      continue;
    }
    const svg = repo.text(posix.join(GUIDE, "diagrams", image)) ?? "";
    const root = svg.match(/<svg\b[^>]*>/)?.[0] ?? "";
    const made = root.match(/\sdata-source-sha256="([0-9a-f]{64})"/)?.[1];
    const drawnBy = root.match(/\sdata-archify="([^"]*)"/)?.[1];
    const drawnIn = root.match(/\sdata-look-sha256="([0-9a-f]{64})"/)?.[1];
    const hash = createHash("sha256").update(repo.text(posix.join(GUIDE, "diagrams", name)) ?? "").digest("hex");
    if (made !== hash) problem(`diagrams/${image}`, null, `was not exported from ${name} as it is now: run svg.mjs ${source[1]}`);
    else if (archify && drawnBy !== archify) problem(`diagrams/${image}`, null, `was drawn by Archify ${drawnBy ?? "(unknown)"}, not the pinned ${archify}: run svg.mjs ${source[1]}`);
    else if (look && drawnIn !== look) problem(`diagrams/${image}`, null, `was not drawn in the current look (look.mjs): run svg.mjs ${source[1]}`);
  }
  return problems;
}

/** Outside links checked when they are added rather than daily: published RFCs. */
export const CHECKED_WHEN_ADDED = /^https:\/\/www\.rfc-editor\.org\/rfc\//;

/** The links the daily check asks about: all but those checked when added. */
export function dailyLinks(links) {
  return new Map([...links].filter(([href]) => !CHECKED_WHEN_ADDED.test(href)));
}

/** The links in `current` that `before` does not have, anchor included: a new section link is checked too. */
export function addedLinks(current, before) {
  return new Map([...current].filter(([href]) => !before.has(href)));
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

// The exceptions file: the outside links that may refuse to be checked, each
// with why it is sound anyway and the date that stops being true by.
export const EXCEPTIONS = join(dirname(fileURLToPath(import.meta.url)), "outside-link-exceptions.json");
const EXCEPTION_KEYS = ["url", "reason", "expires"];

/** Whether value is a real calendar date written YYYY-MM-DD. */
function isDate(value) {
  return typeof value === "string" && /^\d{4}-\d{2}-\d{2}$/.test(value) && new Date(`${value}T00:00:00Z`).toISOString().slice(0, 10) === value;
}

/**
 * Reads the exceptions file's entries on `today` (YYYY-MM-DD, UTC). Returns the
 * ones that hold, by URL, and a problem for each that does not: one with no
 * reason, no expiry date or one that is not a date, one that expired before
 * today, one listed twice, or one that is not an http(s) page. An exception
 * still holds on the day it expires.
 */
export function readExceptions(entries, today) {
  const valid = new Map();
  const problems = [];
  if (entries?.unreadable) return { valid, problems: [`file cannot be read: ${entries.unreadable}`] };
  if (!Array.isArray(entries)) return { valid, problems: ["file must be a JSON array of exceptions, each with url, reason and expires"] };
  const seen = new Set();
  for (const [index, entry] of entries.entries()) {
    const url = typeof entry?.url === "string" ? entry.url : "";
    const before = problems.length;
    const bad = (why) => problems.push(`${url || `entry ${index + 1}`}: ${why}`);
    if (entry === null || typeof entry !== "object" || Array.isArray(entry)) {
      bad("is not an object with url, reason and expires");
      continue;
    }
    for (const key of Object.keys(entry)) if (!EXCEPTION_KEYS.includes(key)) bad(`has a key ${key}: an exception has only url, reason and expires`);
    if (!/^https?:\/\/[^\s#]+$/.test(url)) bad("has no url, or one that is not an http(s) page without an anchor");
    if (seen.has(url)) bad("is listed twice");
    seen.add(url);
    if (typeof entry.reason !== "string" || !entry.reason.trim()) bad("has no reason: say why the link is sound although the site refuses to be checked");
    if (!isDate(entry.expires)) bad("has no expiry date: expires must be a date, YYYY-MM-DD");
    else if (entry.expires < today) bad(`expired on ${entry.expires}: open the link, and set a new date if it is still sound, or replace it`);
    if (problems.length === before) valid.set(url, entry);
  }
  return { valid, problems };
}

/**
 * What the outside links checkOutside found fail the check with: every broken
 * link, every refusal no exception that holds covers, and every problem with an
 * exception, as the lines to report. Also the refusals an exception excuses.
 */
export function outsideVerdict({ broken, refused }, exceptions) {
  const failures = exceptions.problems.map((problem) => `exception ${problem}`);
  const excused = [];
  for (const { url, status, where } of refused) {
    const exception = exceptions.valid.get(url);
    if (exception) excused.push(`excepted until ${exception.expires}, refused to be checked (HTTP ${status}): ${url}: ${exception.reason}  (${where})`);
    else failures.push(`refused to be checked (HTTP ${status}), with no exception: ${url}  (${where})`);
  }
  for (const { href, why, where } of broken) failures.push(`broken: ${href}: ${why}  (${where})`);
  return { failures, excused };
}

/**
 * The outside-link check: asks every link (checkOutside), and judges the answers
 * against the exceptions file's entries on today. It passes when failures is empty.
 */
export async function checkOutsideLinks(links, entries, today, options) {
  const found = await checkOutside(links, options);
  return { ...found, ...outsideVerdict(found, readExceptions(entries, today)) };
}

/** The exceptions file's entries; readExceptions reports a file that cannot be read. */
async function exceptionEntries(path) {
  try {
    return JSON.parse(await readFile(path, "utf8"));
  } catch (error) {
    return { unreadable: `${path}: ${error.message}` };
  }
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
    options: { archify: { type: "string" }, external: { type: "boolean", default: false }, exceptions: { type: "string", default: EXCEPTIONS }, "new-since": { type: "string" } },
  });
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

  const { archify: pin } = JSON.parse(await readFile(new URL("package.json", import.meta.url), "utf8"));
  const problems = checkGuide({ pages, repo, diagramFiles, archify: `${pin.version} ${pin.commit}`, look: lookHash });

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
    const all = outsideLinks(pages);
    let links;
    let scope;
    if (values["new-since"]) {
      const ref = values["new-since"];
      const files = execFileSync("git", ["-C", root, "ls-tree", "-r", "--name-only", ref, "--", GUIDE], { encoding: "utf8" })
        .split("\n").filter((path) => path.endsWith(".md") && !SKIP.has(posix.basename(path)));
      const before = new Map(files.map((path) => [posix.relative(GUIDE, path), execFileSync("git", ["-C", root, "show", `${ref}:${path}`], { encoding: "utf8", maxBuffer: 1 << 26 })]));
      links = addedLinks(all, outsideLinks(before));
      scope = `added since ${ref}`;
    } else {
      links = dailyLinks(all);
      scope = `daily; ${all.size - links.size} RFC links are checked when added`;
    }
    const today = new Date().toISOString().slice(0, 10);
    const { broken, refused, pages: asked, failures, excused } = await checkOutsideLinks(links, await exceptionEntries(values.exceptions), today);
    for (const line of excused) console.log(line);
    for (const line of failures) console.log(line);
    console.log(`${links.size} outside links on ${asked} pages (${scope}): ${broken.length} broken, ${refused.length} refused to be checked (${excused.length} by exception, ${relative(root, values.exceptions)}): ${failures.length} failing`);
    failed ||= failures.length > 0;
  }
  process.exitCode = failed ? 1 : 0;
}

if (import.meta.url === `file://${process.argv[1]}`) await main();

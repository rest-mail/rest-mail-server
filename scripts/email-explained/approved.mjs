// The approved diagrams, and the check that the deployed ones are them (#2120 AC01).
//
// docs/email-explained/diagrams/approved.json records, for every diagram, the
// hash of its source, of the page the website serves for it (bundle.mjs's
// rendering, in the site's look) and of its static image. A diagram is approved
// when it is listed there, and a change to a source, to Archify or to the look
// that is not approved again fails CI. The deployed site is then held to the
// same record.
//
//   node approved.mjs verify   --archify <archify.mjs>   the repository still matches what was approved (CI)
//   node approved.mjs approve  --archify <archify.mjs>   record the diagrams as they are now, after a human has looked
//   node approved.mjs deployed [--site <url>] [--receipt <file>] [--measure]
//                                                        the live site serves the approved diagrams, as the
//                                                        decisions on #822, #823 and #824 describe
//
// `deployed` needs only HTTP. `--measure` also drives Chrome (CHROME=<chrome>)
// at a laptop and a phone width to measure what a reader sees; it is the part
// that says whether the diagrams are readable (the decided floor is 9.5px).
// Only a live site can answer `deployed`: the repository can say what it
// approved, not what is running.
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { readdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { parseArgs } from "node:util";
import { GUIDE, renderDiagramPage, TYPES } from "./bundle.mjs";
import { lookHash } from "./look.mjs";

export const MANIFEST = `${GUIDE}/diagrams/approved.json`;
export const SITE = "https://rest-mail.com";
const SITE_PATH = "/email-explained";

/** The diagrams the owner had redrawn narrower (#824: spf, rest-mail, queue, and open-relay split in two), and the widest canvas they may have. */
export const WIDEST = ["spf", "rest-mail", "queue", "open-relay", "open-relay-cost"];
export const CANVAS_CEILING = 918;
/** The decided floor for a node title, in rendered pixels (#824). */
export const TITLE_FLOOR_PX = 9.5;
export const WIDTHS = { laptop: 1440, phone: 390 };

/** Where the approval comes from: dated decisions, not a claim that these hashes were reviewed one by one. */
export const BASIS = [
  "#822 (2026-10-25, owner): diagrams are same-origin iframes of Archify's own page with ?embed=1",
  "#823 (2026-10-25, owner): one look, Safe's paper, ink and cobalt, in the page and the static image",
  "#824 (2026-10-25, owner): the widest diagrams are redrawn narrower and phones scroll them sideways",
];

export const sha256 = (data) => createHash("sha256").update(data).digest("hex");

const svgWidth = (svg) => Number(svg.match(/<svg\b[^>]*\swidth="(\d+(?:\.\d+)?)"/)?.[1] ?? NaN);

/** What the repository holds for each diagram now: the hashes a manifest records. */
export async function currentDiagrams({ archify, root }) {
  const dir = join(root, GUIDE, "diagrams");
  const found = {};
  for (const file of (await readdir(dir)).sort()) {
    const m = file.match(/^([a-z0-9-]+)\.([a-z]+)\.json$/);
    if (!m || !TYPES.has(m[2])) continue;
    const [, name, type] = m;
    const source = await readFile(join(dir, file));
    const image = await readFile(join(dir, `${name}.svg`), "utf8");
    const { html } = await renderDiagramPage(archify.bin, type, join(dir, file)).catch(() => {
      throw new Error(`${file}: Archify cannot render it (node check.mjs --archify says why)`);
    });
    found[name] = { source: file, source_sha256: sha256(source), page_sha256: sha256(html), image_sha256: sha256(image), canvas_width: svgWidth(image) };
  }
  return found;
}

/** The manifest for diagrams as they are, on `today` (YYYY-MM-DD). */
export function makeManifest({ diagrams, archify, today }) {
  return { format: 1, approved_on: today, archify: archify.id, look_sha256: lookHash, basis: BASIS, diagrams };
}

/**
 * What differs between the approved manifest and the diagrams as they are now.
 * Every line names the diagram, so a drifted one fails by name.
 */
export function driftFromApproved(approved, current, { archify, look = lookHash }) {
  const problems = [];
  if (!approved || typeof approved !== "object" || !approved.diagrams) return [`${MANIFEST}: not an approval record (no diagrams)`];
  if (approved.archify !== archify) problems.push(`approved with Archify ${approved.archify}, but ${archify} is pinned: render again, look, and approve`);
  if (approved.look_sha256 !== look) problems.push("approved in a different look (look.mjs changed): look at the diagrams, and approve");
  for (const [name, now] of Object.entries(current)) {
    const was = approved.diagrams[name];
    if (!was) {
      problems.push(`${name}: not approved (not in ${MANIFEST}): look at it, then run approved.mjs approve`);
      continue;
    }
    if (was.source_sha256 !== now.source_sha256) problems.push(`${name}: source ${now.source} changed since it was approved`);
    else if (was.page_sha256 !== now.page_sha256) problems.push(`${name}: the page the website serves is not the approved one, though the source is`);
    if (was.image_sha256 !== now.image_sha256) problems.push(`${name}: static image changed since it was approved`);
  }
  for (const name of Object.keys(approved.diagrams)) if (!current[name]) problems.push(`${name}: approved, but no longer in the guide: remove it from ${MANIFEST} in the change that removes it`);
  for (const name of WIDEST) {
    const width = approved.diagrams?.[name]?.canvas_width;
    if (!(width <= CANVAS_CEILING)) problems.push(`${name}: canvas ${width}px is past the ${CANVAS_CEILING}px the owner approved for the widest diagrams (#824)`);
  }
  return problems;
}

/** An iframe src is the decided embedding: same origin, this guide's diagram, embed=1, a version. */
export function frameProblem(src) {
  const url = new URL(src, "https://same-origin.invalid");
  if (url.origin !== "https://same-origin.invalid") return "is not a same-origin frame";
  if (!new RegExp(`^${SITE_PATH}/diagrams/[a-z0-9-]+\\.html$`).test(url.pathname)) return `does not frame a guide diagram (${url.pathname})`;
  if (url.searchParams.get("embed") !== "1") return "does not ask for ?embed=1";
  if (!/^[0-9a-f]{12}$/.test(url.searchParams.get("v") ?? "")) return "does not carry the bundle revision (?v=)";
  return null;
}

/** Diagram iframes and any srcdoc in a chapter's HTML. */
export function framesOf(html) {
  const frames = [...html.matchAll(/<iframe\b[^>]*>/gi)].map((m) => ({ tag: m[0], src: m[0].match(/\ssrc="([^"]*)"/)?.[1]?.replace(/&amp;/g, "&") ?? null, srcdoc: /\ssrcdoc=/i.test(m[0]) }));
  return frames;
}

/**
 * Judges what the live site served against the approved manifest.
 * `chapters`: [{ url, frames }] from framesOf. `documents`: name -> { status,
 * sha256, csp, text } for each diagram document fetched.
 */
export function judgeDeployed({ approved, chapters, documents }) {
  const failures = [];
  const framed = new Map();
  for (const { url, frames } of chapters) {
    for (const frame of frames) {
      if (frame.srcdoc || !frame.src) {
        failures.push(`${url}: a diagram frame is not a plain same-origin iframe (srcdoc or no src)`);
        continue;
      }
      const why = frameProblem(frame.src);
      const name = new URL(frame.src, "https://x.invalid").pathname.match(/\/([a-z0-9-]+)\.html$/)?.[1];
      if (why) failures.push(`${url}: frame ${frame.src} ${why}`);
      if (name) framed.set(name, (framed.get(name) ?? new Set()).add(new URL(frame.src, "https://x.invalid").searchParams.get("v")));
    }
  }
  const rows = [];
  for (const [name, was] of Object.entries(approved.diagrams)) {
    const row = { name, approved_sha256: was.page_sha256, deployed_sha256: null, match: false, status: null, frame_ancestors_self: false, look: false, framed: framed.has(name) };
    const doc = documents.get(name);
    if (!framed.has(name)) failures.push(`${name}: no chapter on the site shows this approved diagram`);
    if (!doc) {
      failures.push(`${name}: the diagram document was not fetched`);
      rows.push(row);
      continue;
    }
    row.status = doc.status;
    row.deployed_sha256 = doc.sha256 ?? null;
    row.match = doc.status === 200 && doc.sha256 === was.page_sha256;
    row.frame_ancestors_self = /frame-ancestors[^;]*'self'/.test(doc.csp ?? "");
    row.look = Boolean(doc.text) && ["#f4f1ea", "#15110d", "#1840ff", "JetBrains Mono"].every((token) => doc.text.includes(token));
    if (doc.status !== 200) failures.push(`${name}: HTTP ${doc.status}`);
    else if (!row.match) failures.push(`${name}: deployed page ${doc.sha256} is not the approved ${was.page_sha256}`);
    if (!row.frame_ancestors_self) failures.push(`${name}: served without frame-ancestors 'self'`);
    if (!row.look) failures.push(`${name}: not in Safe's look (paper #f4f1ea, ink #15110d, cobalt #1840ff, JetBrains Mono)`);
    rows.push(row);
  }
  for (const name of framed.keys()) if (!approved.diagrams[name]) failures.push(`${name}: the site frames a diagram that is not approved`);
  return { rows, failures };
}

/**
 * What a reader sees, measured in a browser: `measured` is { laptop|phone: { <diagram>: { minTitlePx,
 * pageOverflowPx, scrolled } } }. The floor is the decided 9.5px: every diagram on a phone, the widest on a laptop.
 */
export function judgeReadability(measured) {
  const failures = [];
  for (const [view, byDiagram] of Object.entries(measured)) {
    for (const [name, m] of Object.entries(byDiagram)) {
      const counts = view === "phone" || WIDEST.includes(name);
      if (counts && !(m.minTitlePx >= TITLE_FLOOR_PX)) failures.push(`${name} at ${view}: smallest title is ${Number(m.minTitlePx).toFixed(3)}px, under the ${TITLE_FLOOR_PX}px floor (#824)`);
      if (m.pageOverflowPx > 0) failures.push(`${name} at ${view}: the page scrolls sideways by ${m.pageOverflowPx}px; only the diagram's own box may`);
    }
  }
  const swipe = measured.phone?.[WIDEST[0]]?.swipe;
  if (!swipe || !(swipe.frameScrolled > 0)) failures.push("a sideways swipe over the diagram did not scroll it");
  else if (swipe.pageScrolledX !== 0) failures.push("a sideways swipe over the diagram scrolled the page");
  return failures;
}

const RECEIPT_KEYS = ["kind", "format", "checked_at", "site", "approved_on", "approved_manifest_sha256", "bundle_revisions", "diagrams", "readability", "failures", "verdict"];

/** Whether a receipt is complete: what an owner can attach to #2120 as the evidence of one run. */
export function validateReceipt(receipt) {
  const problems = [];
  if (!receipt || typeof receipt !== "object") return ["not an object"];
  for (const key of RECEIPT_KEYS) if (!(key in receipt)) problems.push(`no ${key}`);
  if (receipt.kind !== "email-explained-deployed-diagram-receipt") problems.push("kind is not email-explained-deployed-diagram-receipt");
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/.test(receipt.checked_at ?? "")) problems.push("checked_at is not a timestamp");
  if (!Array.isArray(receipt.diagrams) || receipt.diagrams.length === 0) problems.push("no diagrams were compared");
  if (!["PASS", "FAIL"].includes(receipt.verdict)) problems.push("verdict is neither PASS nor FAIL");
  if (receipt.verdict === "PASS" && (receipt.failures?.length || receipt.readability?.status !== "measured")) problems.push("PASS needs no failures and a measured readability section: a run that did not measure cannot pass");
  if (receipt.verdict === "FAIL" && !receipt.failures?.length) problems.push("FAIL names no failure");
  return problems;
}

async function archifyFor(path) {
  if (!path) throw new Error("--archify <archify.mjs> is required: run node fetch-archify.mjs <dir> and pass the path it prints");
  const { archify: pin } = JSON.parse(await readFile(new URL("package.json", import.meta.url), "utf8"));
  return { bin: path, id: `${pin.version} ${pin.commit}` };
}

async function get(url) {
  const response = await fetch(url, { redirect: "follow", signal: AbortSignal.timeout(30_000), headers: { "user-agent": "rest-mail-diagram-check" } });
  const bytes = Buffer.from(await response.arrayBuffer());
  return { status: response.status, bytes, text: bytes.toString("utf8"), csp: response.headers.get("content-security-policy") ?? "" };
}

async function deployed({ site, receiptPath, measure, approvedText, approved }) {
  const index = await get(`${site}${SITE_PATH}`);
  if (index.status !== 200) throw new Error(`${site}${SITE_PATH} answered HTTP ${index.status}`);
  const links = new Set([SITE_PATH]);
  for (const m of index.text.matchAll(/href="(\/email-explained\/[^"#?]+)"/g)) links.add(m[1].replace(/\/$/, ""));
  const chapters = [];
  for (const path of [...links].sort()) {
    const page = path === SITE_PATH ? index : await get(`${site}${path}`);
    if (page.status !== 200) {
      chapters.push({ url: `${site}${path}`, frames: [], status: page.status });
      continue;
    }
    chapters.push({ url: `${site}${path}`, frames: framesOf(page.text), status: 200 });
  }
  const bad = chapters.filter((c) => c.status && c.status !== 200).map((c) => `${c.url}: HTTP ${c.status}`);
  const documents = new Map();
  for (const name of Object.keys(approved.diagrams)) {
    const doc = await get(`${site}${SITE_PATH}/diagrams/${name}.html`).catch(() => null);
    if (doc) documents.set(name, { status: doc.status, sha256: sha256(doc.bytes), csp: doc.csp, text: doc.text });
  }
  const verdict = judgeDeployed({ approved, chapters, documents });
  const revisions = [...new Set(chapters.flatMap((c) => c.frames.map((f) => (f.src ? new URL(f.src, site).searchParams.get("v") : null)).filter(Boolean)))].sort();
  const failures = [...bad, ...verdict.failures];

  let readability = { status: "not measured", reason: "run with --measure and CHROME=<chrome> to measure title sizes and sideways scrolling" };
  if (measure) {
    const { measureSite } = await import("./measure.mjs");
    const frameChapters = Object.fromEntries(
      WIDEST.concat([...Object.keys(approved.diagrams)]).filter((v, i, a) => a.indexOf(v) === i).map((name) => [name, chapters.find((c) => c.frames.some((f) => f.src?.includes(`/${name}.html`)))?.url]),
    );
    const measured = await measureSite({ chrome: process.env.CHROME, frameChapters, widths: WIDTHS });
    const readFailures = judgeReadability(measured);
    failures.push(...readFailures);
    readability = { status: "measured", floor_px: TITLE_FLOOR_PX, widths: WIDTHS, measured, failures: readFailures };
  }

  const receipt = {
    kind: "email-explained-deployed-diagram-receipt",
    format: 1,
    checked_at: new Date().toISOString(),
    site,
    approved_on: approved.approved_on,
    approved_manifest_sha256: sha256(approvedText),
    bundle_revisions: revisions,
    diagrams: verdict.rows,
    readability,
    failures,
    verdict: failures.length === 0 && readability.status === "measured" ? "PASS" : "FAIL",
  };
  if (failures.length === 0 && readability.status !== "measured") receipt.failures.push("readability was not measured, so the run cannot pass (run with --measure)");
  const problems = validateReceipt(receipt);
  if (problems.length) throw new Error(`the receipt is not complete: ${problems.join("; ")}`);
  if (receiptPath) await writeFile(receiptPath, `${JSON.stringify(receipt, null, 2)}\n`);
  return receipt;
}

async function main() {
  const { positionals, values } = parseArgs({
    options: { archify: { type: "string" }, site: { type: "string", default: SITE }, receipt: { type: "string" }, measure: { type: "boolean", default: false } },
    allowPositionals: true,
  });
  const [command] = positionals;
  const root = execFileSync("git", ["rev-parse", "--show-toplevel"], { encoding: "utf8" }).trim();
  const manifestPath = join(root, MANIFEST);
  const today = new Date().toISOString().slice(0, 10);

  if (command === "approve") {
    const archify = await archifyFor(values.archify);
    const manifest = makeManifest({ diagrams: await currentDiagrams({ archify, root }), archify, today });
    await writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
    console.log(`${MANIFEST}: ${Object.keys(manifest.diagrams).length} diagrams recorded on ${today}`);
  } else if (command === "verify") {
    const archify = await archifyFor(values.archify);
    const approved = JSON.parse(await readFile(manifestPath, "utf8"));
    const problems = driftFromApproved(approved, await currentDiagrams({ archify, root }), { archify: archify.id });
    for (const line of problems) console.log(`${MANIFEST}: ${line}`);
    console.log(`${Object.keys(approved.diagrams ?? {}).length} approved diagrams: ${problems.length} problem${problems.length === 1 ? "" : "s"}`);
    process.exitCode = problems.length ? 1 : 0;
  } else if (command === "deployed") {
    const approvedText = await readFile(manifestPath, "utf8");
    const receipt = await deployed({ site: values.site.replace(/\/$/, ""), receiptPath: values.receipt, measure: values.measure, approvedText, approved: JSON.parse(approvedText) });
    for (const line of receipt.failures) console.log(`FAIL ${line}`);
    console.log(`${receipt.verdict}: ${receipt.diagrams.filter((d) => d.match).length}/${receipt.diagrams.length} deployed diagrams are the approved ones, bundle revision ${receipt.bundle_revisions.join(", ") || "(none seen)"}, readability ${receipt.readability.status}`);
    process.exitCode = receipt.verdict === "PASS" ? 0 : 1;
  } else {
    console.error("usage: node approved.mjs verify|approve --archify <archify.mjs>  |  node approved.mjs deployed [--site <url>] [--receipt <file>] [--measure]");
    process.exitCode = 2;
  }
}

if (import.meta.url === `file://${process.argv[1]}`) await main();

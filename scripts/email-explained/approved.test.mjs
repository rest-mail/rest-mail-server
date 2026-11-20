// #2120 AC01: the deployed diagrams match the approved artifacts. Each test name
// starts "AC01:" so the criterion is found by name.
import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { CANVAS_CEILING, driftFromApproved, frameProblem, framesOf, judgeDeployed, judgeReadability, MANIFEST, sha256, TITLE_FLOOR_PX, validateReceipt, WIDEST } from "./approved.mjs";
import { lookHash } from "./look.mjs";

const root = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const approved = JSON.parse(readFileSync(join(root, MANIFEST), "utf8"));
const ARCHIFY = JSON.parse(readFileSync(new URL("package.json", import.meta.url), "utf8")).archify;
const pinned = `${ARCHIFY.version} ${ARCHIFY.commit}`;

// The diagrams as the repository holds them now: sources and images are files,
// so this part of the check needs no Archify. (The page each source renders to
// is hashed by `node approved.mjs verify --archify`, which CI runs.)
const dir = join(root, "docs/email-explained/diagrams");
function repoDiagrams() {
  const found = {};
  for (const file of readdirSync(dir).sort()) {
    const m = file.match(/^([a-z0-9-]+)\.(architecture|workflow|sequence|dataflow|lifecycle|erd|tree|class|timeline|waterfall)\.json$/);
    if (!m) continue;
    const image = readFileSync(join(dir, `${m[1]}.svg`), "utf8");
    found[m[1]] = { source: file, source_sha256: sha256(readFileSync(join(dir, file))), image_sha256: sha256(image), canvas_width: Number(image.match(/<svg\b[^>]*\swidth="(\d+(?:\.\d+)?)"/)[1]) };
  }
  return found;
}
// Page hashes are not available without Archify; take the approved ones so only source and image can differ.
const withApprovedPages = (diagrams) => Object.fromEntries(Object.entries(diagrams).map(([name, d]) => [name, { ...d, page_sha256: approved.diagrams[name]?.page_sha256 }]));

test("AC01: every diagram source and static image in the repository is the one approved.json records", () => {
  const problems = driftFromApproved(approved, withApprovedPages(repoDiagrams()), { archify: pinned });
  assert.deepEqual(problems, []);
  assert.ok(Object.keys(approved.diagrams).length >= 27, "the manifest lists every diagram");
});

test("AC01: a source edited after approval fails, naming the diagram", () => {
  const current = withApprovedPages(repoDiagrams());
  current.spf = { ...current.spf, source_sha256: sha256("redrawn") };
  const problems = driftFromApproved(approved, current, { archify: pinned });
  assert.equal(problems.length, 1);
  assert.match(problems[0], /^spf: source spf\.workflow\.json changed since it was approved/);
});

test("AC01: a page that renders differently from the approved one fails although its source is the same", () => {
  const current = withApprovedPages(repoDiagrams());
  current.dkim = { ...current.dkim, page_sha256: sha256("another rendering") };
  assert.match(driftFromApproved(approved, current, { archify: pinned }).join("\n"), /dkim: the page the website serves is not the approved one/);
});

test("AC01: a changed static image, a new diagram and a removed one each fail", () => {
  const current = withApprovedPages(repoDiagrams());
  current.arc = { ...current.arc, image_sha256: sha256("new image") };
  current.brand_new = { ...current.arc, source: "brand-new.tree.json" };
  delete current.queue;
  const text = driftFromApproved(approved, current, { archify: pinned }).join("\n");
  assert.match(text, /arc: static image changed/);
  assert.match(text, /brand_new: not approved/);
  assert.match(text, /queue: approved, but no longer in the guide/);
});

test("AC01: a different Archify or a different look is not the approved rendering", () => {
  const current = withApprovedPages(repoDiagrams());
  assert.match(driftFromApproved(approved, current, { archify: "9.9.9 deadbeef" }).join("\n"), /approved with Archify .* but 9\.9\.9 deadbeef is pinned/);
  assert.match(driftFromApproved(approved, current, { archify: pinned, look: "0".repeat(64) }).join("\n"), /different look/);
  assert.equal(approved.look_sha256, lookHash, "the manifest records the look the repository has");
});

test("AC01: a widest diagram past the approved canvas width fails (#824)", () => {
  const wider = structuredClone(approved);
  wider.diagrams.spf.canvas_width = CANVAS_CEILING + 1;
  assert.match(driftFromApproved(wider, withApprovedPages(repoDiagrams()), { archify: pinned }).join("\n"), /spf: canvas 919px is past the 918px/);
  for (const name of WIDEST) assert.ok(approved.diagrams[name].canvas_width <= CANVAS_CEILING, `${name} is within the ceiling`);
});

// What the site serves, built from the approved pages: the deployed side of the check.
const LOOK_TEXT = "paper #f4f1ea ink #15110d cobalt #1840ff JetBrains Mono";
const served = (overrides = {}) =>
  new Map(Object.entries(approved.diagrams).map(([name, d]) => [name, { status: 200, sha256: d.page_sha256, csp: "default-src 'none'; frame-ancestors 'self'", text: LOOK_TEXT, ...overrides[name] }]));
const chapters = (src = (name) => `/email-explained/diagrams/${name}.html?v=0123456789ab&amp;embed=1`) => [
  { url: "https://site/email-explained/x", frames: framesOf(Object.keys(approved.diagrams).map((n) => `<iframe src="${src(n)}" title="t"></iframe>`).join("")) },
];

test("AC01: a site serving every approved page, framed with embed=1 and frame-ancestors 'self', passes", () => {
  const { failures, rows } = judgeDeployed({ approved, chapters: chapters(), documents: served() });
  assert.deepEqual(failures, []);
  assert.equal(rows.filter((r) => r.match).length, Object.keys(approved.diagrams).length);
});

test("AC01: one deployed diagram that is not the approved page fails, naming it and both hashes", () => {
  const { failures } = judgeDeployed({ approved, chapters: chapters(), documents: served({ spf: { sha256: sha256("the stale one") } }) });
  assert.equal(failures.length, 1);
  assert.match(failures[0], new RegExp(`^spf: deployed page ${sha256("the stale one")} is not the approved ${approved.diagrams.spf.page_sha256}`));
});

test("AC01: a diagram missing from the site, answering an error, or served without frame-ancestors fails", () => {
  const docs = served({ dns: { status: 404 }, dkim: { csp: "default-src 'none'" } });
  docs.delete("arc");
  const text = judgeDeployed({ approved, chapters: chapters(), documents: docs }).failures.join("\n");
  assert.match(text, /arc: the diagram document was not fetched/);
  assert.match(text, /dns: HTTP 404/);
  assert.match(text, /dkim: served without frame-ancestors 'self'/);
});

test("AC01: a page without Safe's paper, ink, cobalt and typeface is not in the shared look", () => {
  const { failures } = judgeDeployed({ approved, chapters: chapters(), documents: served({ smtp: { text: "plain" } }) });
  assert.match(failures.join("\n"), /smtp: not in Safe's look/);
});

test("AC01: iframe mode is required: srcdoc, no embed=1, another origin and an unversioned frame each fail", () => {
  assert.equal(frameProblem("/email-explained/diagrams/spf.html?v=0123456789ab&embed=1"), null);
  assert.match(frameProblem("/email-explained/diagrams/spf.html?v=0123456789ab"), /embed=1/);
  assert.match(frameProblem("https://elsewhere.example/email-explained/diagrams/spf.html?v=0123456789ab&embed=1"), /same-origin/);
  assert.match(frameProblem("/email-explained/diagrams/spf.html?embed=1"), /revision/);
  const srcdoc = [{ url: "https://site/c", frames: framesOf('<iframe srcdoc="<svg></svg>"></iframe>') }];
  assert.match(judgeDeployed({ approved, chapters: srcdoc, documents: served() }).failures.join("\n"), /not a plain same-origin iframe/);
});

test("AC01: an approved diagram no chapter frames, and a framed diagram nobody approved, both fail", () => {
  const some = Object.keys(approved.diagrams).filter((n) => n !== "unsubscribe");
  const page = [{ url: "https://site/c", frames: framesOf(some.concat("surprise").map((n) => `<iframe src="/email-explained/diagrams/${n}.html?v=0123456789ab&embed=1"></iframe>`).join("")) }];
  const text = judgeDeployed({ approved, chapters: page, documents: served() }).failures.join("\n");
  assert.match(text, /unsubscribe: no chapter on the site shows this approved diagram/);
  assert.match(text, /surprise: the site frames a diagram that is not approved/);
});

const good = (px = 10) => ({ minTitlePx: px, pageOverflowPx: 0 });
test("AC01: readability: a title under 9.5px fails on a phone for any diagram and on a laptop for the widest", () => {
  const measured = { laptop: { spf: good(8.178), arc: good(8.4) }, phone: { spf: { ...good(9.6), swipe: { frameScrolled: 300, pageScrolledX: 0 } }, arc: good(8.4) } };
  const text = judgeReadability(measured).join("\n");
  assert.match(text, /spf at laptop: smallest title is 8\.178px, under the 9\.5px floor/);
  assert.match(text, /arc at phone/);
  assert.doesNotMatch(text, /arc at laptop/, "a diagram that is not among the widest has no laptop floor");
  assert.equal(TITLE_FLOOR_PX, 9.5);
});

test("AC01: readability: a page that scrolls sideways, or a swipe that moves the page or nothing, fails", () => {
  const swipe = (s) => ({ phone: { spf: { ...good(), swipe: s } } });
  assert.deepEqual(judgeReadability(swipe({ frameScrolled: 300, pageScrolledX: 0 })), []);
  assert.match(judgeReadability(swipe({ frameScrolled: 0, pageScrolledX: 0 })).join(), /did not scroll it/);
  assert.match(judgeReadability(swipe({ frameScrolled: 300, pageScrolledX: 20 })).join(), /scrolled the page/);
  assert.match(judgeReadability({ phone: { spf: { ...good(), pageOverflowPx: 12, swipe: { frameScrolled: 300, pageScrolledX: 0 } } } }).join(), /the page scrolls sideways by 12px/);
});

test("AC01: a receipt that did not measure readability cannot say PASS, and a complete one validates", () => {
  const receipt = {
    kind: "email-explained-deployed-diagram-receipt",
    format: 1,
    checked_at: "2026-11-20T10:00:00.000Z",
    site: "https://rest-mail.com",
    approved_on: approved.approved_on,
    approved_manifest_sha256: "0".repeat(64),
    bundle_revisions: ["0123456789ab"],
    diagrams: [{ name: "spf", match: true }],
    readability: { status: "measured" },
    failures: [],
    verdict: "PASS",
  };
  assert.deepEqual(validateReceipt(receipt), []);
  assert.match(validateReceipt({ ...receipt, readability: { status: "not measured" } }).join(), /a run that did not measure cannot pass/);
  assert.match(validateReceipt({ ...receipt, verdict: "FAIL" }).join(), /FAIL names no failure/);
  assert.match(validateReceipt({ ...receipt, checked_at: "yesterday" }).join(), /checked_at is not a timestamp/);
  const { diagrams, ...without } = receipt;
  assert.match(validateReceipt(without).join(), /no diagrams/);
});

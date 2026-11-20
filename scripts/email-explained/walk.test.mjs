// #2120 AC02: the five customer guides are published, and the end-to-end walk
// has its steps, its named installation and a receipt that cannot pass early.
// Each test name starts "AC02:" so the criterion is found by name. Nothing here
// says a walk ran: the receipts below are fixtures that show what the check
// accepts and refuses.
import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { compileChapter, GUIDE, readingOrder, splitFrontMatter } from "./bundle.mjs";
import { checkReceipt, CUSTOMER_GUIDES, matches, newReceipt, recordStep, specProblems } from "./walk.mjs";
import { guideSlugs, unpublished } from "./published.mjs";

const root = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const read = (path) => (existsSync(join(root, path)) ? readFileSync(join(root, path), "utf8") : null);
const spec = JSON.parse(readFileSync(new URL("walk-spec.json", import.meta.url), "utf8"));
const guideFile = (slug) => `${GUIDE}/guides/${slug}.md`;

test("AC02: the five customer guides exist, are listed on the guide's front page and in the docs index, and compile into the bundle", () => {
  const readme = read(`${GUIDE}/README.md`);
  const docsIndex = read("docs/README.md");
  assert.equal(CUSTOMER_GUIDES.length, 5);
  const files = new Set(["README.md", ...CUSTOMER_GUIDES.map((s) => `guides/${s}.md`)]);
  const order = readingOrder(readme, [...files]);
  for (const slug of CUSTOMER_GUIDES) {
    const text = read(guideFile(slug));
    assert.ok(text, `${guideFile(slug)} exists`);
    assert.ok(readme.includes(`(guides/${slug}.md)`), `${slug} is linked from the front page's Part 5`);
    assert.ok(order.includes(`guides/${slug}.md`), `${slug} has a place in the reading order`);
    const { meta } = splitFrontMatter(text);
    assert.equal(meta.part, "Using a REST Mail domain", `${slug} is in Part 5`);
    assert.match(text, /^## Sources\s*$/m, `${slug} cites its sources`);
    const chapter = compileChapter({ file: `guides/${slug}.md`, text, commit: "0".repeat(40), files, diagrams: new Map() });
    assert.equal(chapter.slug, `guides/${slug}`);
    assert.ok(chapter.sections.length > 0, `${slug} builds into sections`);
  }
  assert.ok(docsIndex.includes("email-explained/guides/"), "docs/README.md points customers at the guides");
  assert.deepEqual(guideSlugs(), CUSTOMER_GUIDES.map((s) => `guides/${s}`));
});

test("AC02: publication is checked in both places, and a guide missing from the bundle or answering 404 on the site is named", () => {
  const all = guideSlugs();
  const ok = new Map(all.map((s) => [s, 200]));
  assert.deepEqual(unpublished({ bundleSlugs: all, siteStatus: ok }), []);
  const text = unpublished({ bundleSlugs: all.slice(1), siteStatus: new Map(all.map((s) => [s, 404])) }).join("\n");
  assert.match(text, /guides\/setting-up-your-first-domain: not a chapter of the newest published bundle/);
  assert.match(text, /guides\/why-mail-goes-to-spam: the site answers HTTP 404, not 200/);
});

test("AC02: the walk names a disposable installation and every step follows a real heading of its guide, with a deliberate failure in each guide", () => {
  assert.deepEqual(specProblems(spec, (file) => read(`${GUIDE}/${file}`)), []);
  assert.equal(spec.installation.name, "guidewalk");
  assert.ok(spec.installation.down.some((c) => /topology:down/.test(c)), "the installation can be removed by one command");
  assert.ok(spec.installation.substitutions.length > 0, "what stands in for the real thing is said up front");
});

test("AC02: a step that names a heading the guide does not have, a guide without a deliberate failure, or a missing guide fails the spec", () => {
  const broken = structuredClone(spec);
  broken.guides[0].steps[0].anchor = "no-such-heading";
  broken.guides[1].steps = broken.guides[1].steps.filter((s) => !s.failure);
  broken.guides.pop();
  const text = specProblems(broken, (file) => read(`${GUIDE}/${file}`)).join("\n");
  assert.match(text, /domain-01: #no-such-heading is not a heading of guides\/setting-up-your-first-domain\.md/);
  assert.match(text, /connecting-your-mail-app: no deliberate failure example/);
  assert.match(text, /the walk has no steps for the guide why-mail-goes-to-spam/);
});

const vars = { domain: "walk.restmail.test", host: "mail.walk.restmail.test", ip: "10.99.0.5", receiver: "ref.walk.restmail.test" };
const versions = { server: "0.0.0-fixture", guide_commit: "a".repeat(40), tools: { dig: "9.x" } };
const T0 = "2026-11-20T10:00:00.000Z";
const T1 = "2026-11-20T11:00:00.000Z";
const steps = spec.guides.flatMap((g) => g.steps);
// An observation that satisfies a step, built from its own expect, for fixtures only.
const sample = (step) => `FIXTURE saw ${step.expect ? step.expect.split("|")[0].replace(/\\[.]/g, ".").replace(/\[\\s\\S\]\*/g, " ").replace(/\{(\w+)\}/g, (_, k) => vars[k]).replace(/\(fail\|softfail\)/, "fail").replace(/[()\\]/g, "") : "it work"}`;
function walked() {
  const receipt = newReceipt({ spec, vars, versions, now: T0 });
  for (const step of steps) recordStep(receipt, spec, step.id, { status: "pass", observed: sample(step) }, T1);
  return receipt;
}
const check = (receipt, opts) => checkReceipt(receipt, spec, opts);

test("AC02: a receipt with nothing walked says NOT WALKED and names every open step", () => {
  const { verdict, problems } = check(newReceipt({ spec, vars, versions, now: T0 }));
  assert.equal(verdict, "NOT WALKED");
  for (const step of steps) assert.ok(problems.some((p) => p.startsWith(`${step.id} `) && /not walked/.test(p)), `${step.id} is named`);
});

test("AC02: a fixture receipt with every step observed after the walk began, with the versions, says WALKED", () => {
  assert.deepEqual(check(walked(), { guideCommit: versions.guide_commit }), { verdict: "WALKED", problems: [] });
});

test("AC02: one open step, a missing version, an unnamed or unattested installation, or guides that changed since each fail the receipt", () => {
  const open = walked();
  open.steps["app-05"] = { status: "pending" };
  assert.match(check(open).problems.join("\n"), /app-05 \(connecting-your-mail-app\): not walked/);

  const noVersion = walked();
  noVersion.versions.server = "";
  assert.match(check(noVersion).problems.join(), /no server version recorded/);

  const real = walked();
  real.installation.kind = "real";
  assert.match(check(real).problems.join(), /needs owner_attests_disposable: true/);
  real.installation.owner_attests_disposable = true;
  assert.deepEqual(check(real).problems, []);

  const unnamed = walked();
  unnamed.installation.name = "";
  assert.match(check(unnamed).problems.join(), /no name/);

  assert.match(check(walked(), { guideCommit: "b".repeat(40) }).problems.join(), /followed the guides at a{40}, not at b{40}/);
});

test("AC02: a deliberate failure passes only when it shows the text the guide promises, and cannot be recorded or accepted otherwise", () => {
  const step = steps.find((s) => s.id === "domain-04");
  const receipt = newReceipt({ spec, vars, versions, now: T0 });
  assert.throws(() => recordStep(receipt, spec, step.id, { status: "pass", observed: "It saved without complaint" }, T1), /does not show no currently valid certificate/);
  assert.ok(matches(step, "This domain has no currently valid certificate, so it cannot go live.", vars));
  const forged = walked();
  forged.steps["domain-04"].observed = "It saved without complaint";
  assert.match(check(forged).problems.join(), /domain-04 \(setting-up-your-first-domain\): the observation does not show no currently valid certificate/);
});

test("AC02: a failed step blocks the walk until corrected and walked again, and a failure or substitution must say why", () => {
  const receipt = newReceipt({ spec, vars, versions, now: T0 });
  assert.throws(() => recordStep(receipt, spec, "auth-03", { status: "fail", observed: "TXT shows two SPF records" }, T1), /says what was corrected/);
  assert.throws(() => recordStep(receipt, spec, "spam-03", { status: "substituted", observed: "no public blocklist is reachable" }, T1), /what stood in for the real thing/);
  assert.throws(() => recordStep(receipt, spec, "auth-03", { status: "pass", observed: "" }, T1), /say what was observed/);
  assert.throws(() => recordStep(receipt, spec, "nope-01", { status: "pass", observed: "x" }, T1), /no step nope-01/);
  const failed = walked();
  recordStep(failed, spec, "auth-03", { status: "fail", observed: "TXT shows two SPF records", correction: "guide said replace, server appended; fixed in #0" }, T1);
  assert.match(check(failed).problems.join(), /auth-03 \(setting-up-dkim-spf-dmarc\): failed: the guide or REST Mail is wrong/);
  recordStep(failed, spec, "spam-03", { status: "substituted", observed: "private address, no public blocklist answers", reason: "the topology is private" }, T1);
  assert.deepEqual(check(failed).problems.filter((p) => p.startsWith("spam-03")), []);
});

test("AC02: an observation made before the walk began, or with no timestamp, is not accepted", () => {
  const early = walked();
  early.steps["bounce-02"].at = "2026-11-20T09:00:00.000Z";
  delete early.steps["bounce-03"].at;
  const text = check(early).problems.join("\n");
  assert.match(text, /bounce-02 .*observed before the walk began/);
  assert.match(text, /bounce-03 .*no timestamp/);
});

test("AC02: a guide whose deliberate failures were all substituted has not shown a failure example", () => {
  const receipt = walked();
  for (const step of steps.filter((s) => s.failure && s.id.startsWith("auth-"))) {
    receipt.steps[step.id] = { status: "substituted", observed: "not done", at: T1, reason: "fixture" };
  }
  assert.match(check(receipt).problems.join(), /setting-up-dkim-spf-dmarc: no deliberate failure example was seen/);
});

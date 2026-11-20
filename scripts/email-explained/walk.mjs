// The end-to-end walk of the five customer guides, and its receipt (#2120 AC02).
//
// "Followed end to end on a named disposable installation" is something only a
// person with an installation can do, once. This ships what that run needs: the
// exact steps (walk-spec.json, each tied to a heading of its guide), the name of
// the installation, and a receipt that cannot say WALKED until every step has
// been observed, with the time, the versions and what was seen. It does not
// pretend the walk ran: a receipt with a step left open says NOT WALKED and
// names it.
//
//   node walk.mjs plan                                       the installation and every step
//   node walk.mjs init  --receipt FILE --domain D --host H --ip I --receiver R [--server-version V]
//   node walk.mjs probe  --receipt FILE STEP                 run the step's command and record what it printed
//   node walk.mjs record --receipt FILE STEP --status pass|fail|substituted --observed TEXT
//                        [--evidence PATH] [--correction TEXT] [--reason TEXT]
//   node walk.mjs check  --receipt FILE                      WALKED or NOT WALKED, and why
import { execFileSync, spawnSync } from "node:child_process";
import { readFile, writeFile } from "node:fs/promises";
import { parseArgs } from "node:util";
import { anchorsOf } from "./check.mjs";
import { GUIDE } from "./bundle.mjs";

export const SPEC = new URL("walk-spec.json", import.meta.url);

/** The five customer guides, as the owner's #1588 lists them. */
export const CUSTOMER_GUIDES = [
  "setting-up-your-first-domain",
  "connecting-your-mail-app",
  "troubleshooting-bouncing-mail",
  "setting-up-dkim-spf-dmarc",
  "why-mail-goes-to-spam",
];

const STATUSES = ["pass", "fail", "substituted"];
const KINDS = ["dns", "client", "receiver", "screen", "api"];
const git = (...args) => execFileSync("git", args, { encoding: "utf8" }).trim();

/** The spec's own consistency: the five guides, real headings, unique steps, a deliberate failure in each. */
export function specProblems(spec, readGuide) {
  const problems = [];
  const slugs = spec.guides?.map((g) => g.slug) ?? [];
  for (const slug of CUSTOMER_GUIDES) if (!slugs.includes(slug)) problems.push(`the walk has no steps for the guide ${slug}`);
  for (const slug of slugs) if (!CUSTOMER_GUIDES.includes(slug)) problems.push(`${slug} is not one of the five customer guides`);
  if (!/^[a-z0-9][a-z0-9-]*$/.test(spec.installation?.name ?? "")) problems.push("the installation has no name (lowercase letters, digits and hyphens)");
  const ids = new Set();
  for (const guide of spec.guides ?? []) {
    const text = readGuide(guide.file);
    if (text == null) {
      problems.push(`${guide.slug}: ${guide.file} does not exist`);
      continue;
    }
    const anchors = anchorsOf(text);
    if (!guide.steps?.length) problems.push(`${guide.slug}: no steps`);
    if (!guide.steps?.some((s) => s.failure)) problems.push(`${guide.slug}: no deliberate failure example, which the criterion asks for`);
    for (const step of guide.steps ?? []) {
      if (!/^[a-z]+-\d\d$/.test(step.id ?? "")) problems.push(`${guide.slug}: step id ${step.id} is not <word>-NN`);
      if (ids.has(step.id)) problems.push(`${step.id}: used twice`);
      ids.add(step.id);
      if (!anchors.has(step.anchor)) problems.push(`${step.id}: #${step.anchor} is not a heading of ${guide.file}`);
      if (!KINDS.includes(step.kind)) problems.push(`${step.id}: kind ${step.kind} is not one of ${KINDS.join(", ")}`);
      for (const key of ["do", "observe"]) if (!step[key]?.trim()) problems.push(`${step.id}: no ${key}`);
      if (step.failure && !step.expect) problems.push(`${step.id}: a deliberate failure needs the text it must produce (expect)`);
      if (step.expect) {
        try {
          new RegExp(step.expect);
        } catch {
          problems.push(`${step.id}: expect is not a regular expression`);
        }
      }
    }
  }
  return problems;
}

const escapeRegExp = (text) => text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
const fill = (text, vars, escape = (v) => v) => text.replace(/\{(domain|host|ip|receiver)\}/g, (_, key) => escape(vars?.[key] ?? `{${key}}`));
/** Whether an observation shows what the step expects, with the installation's names filled in. */
export const matches = (step, observed, vars) => !step.expect || new RegExp(fill(step.expect, vars, escapeRegExp), "i").test(observed ?? "");

/** A receipt with every step open. Versions are recorded by the walker, not guessed here. */
export function newReceipt({ spec, vars, versions, now }) {
  return {
    kind: "email-explained-guide-walk-receipt",
    format: 1,
    started_at: now,
    installation: { name: spec.installation.name, kind: spec.installation.kind, vars: { ...vars }, substitutions: [...spec.installation.substitutions] },
    versions: structuredClone(versions),
    steps: Object.fromEntries(spec.guides.flatMap((g) => g.steps.map((s) => [s.id, { status: "pending" }]))),
  };
}

/** Records one step's result. A failure or a substitution must say why; a deliberate failure must show its text. */
export function recordStep(receipt, spec, id, result, now) {
  const step = spec.guides.flatMap((g) => g.steps).find((s) => s.id === id);
  if (!step) throw new Error(`no step ${id} in the walk`);
  const { status, observed = "", evidence, correction, reason } = result;
  if (!STATUSES.includes(status)) throw new Error(`status ${status} is not one of ${STATUSES.join(", ")}`);
  if (!observed.trim()) throw new Error(`${id}: say what was observed`);
  if (status === "pass" && !matches(step, observed, receipt.installation.vars)) throw new Error(`${id}: the observation does not show ${fill(step.expect, receipt.installation.vars)}, so it cannot be recorded as a pass`);
  if (status === "fail" && !correction?.trim()) throw new Error(`${id}: a failed step says what was corrected, in the guide or in REST Mail`);
  if (status === "substituted" && !reason?.trim()) throw new Error(`${id}: a substituted step says what stood in for the real thing, and why`);
  receipt.steps[id] = { status, observed, at: now, ...(evidence && { evidence }), ...(correction && { correction }), ...(reason && { reason }) };
  return receipt;
}

const VERSION_KEYS = ["server", "guide_commit", "tools"];

/**
 * Whether the receipt shows the walk was done. Returns { verdict, problems }:
 * WALKED only when the installation is named and disposable, the versions are
 * recorded, and every step of every guide has an observation made after the
 * walk began, with each deliberate failure showing the text the guide promises.
 */
export function checkReceipt(receipt, spec, { guideCommit } = {}) {
  const problems = [];
  if (receipt?.kind !== "email-explained-guide-walk-receipt") return { verdict: "NOT WALKED", problems: ["not a guide-walk receipt"] };
  const inst = receipt.installation ?? {};
  if (!/^[a-z0-9][a-z0-9-]*$/.test(inst.name ?? "")) problems.push("the installation has no name");
  if (inst.kind === "real" && inst.owner_attests_disposable !== true) problems.push("a real installation needs owner_attests_disposable: true");
  else if (!["topology", "real"].includes(inst.kind)) problems.push(`installation kind ${inst.kind} is neither topology nor real`);
  for (const key of ["domain", "host", "ip", "receiver"]) if (!inst.vars?.[key]) problems.push(`the installation does not record its ${key}`);
  for (const key of VERSION_KEYS) if (!receipt.versions?.[key] || (key === "tools" && !Object.keys(receipt.versions[key]).length)) problems.push(`no ${key} version recorded`);
  if (guideCommit && receipt.versions?.guide_commit && receipt.versions.guide_commit !== guideCommit) problems.push(`the walk followed the guides at ${receipt.versions.guide_commit}, not at ${guideCommit} (walk again, or say which instructions changed)`);
  const began = Date.parse(receipt.started_at);
  if (Number.isNaN(began)) problems.push("started_at is not a timestamp");
  for (const guide of spec.guides) {
    for (const step of guide.steps) {
      const r = receipt.steps?.[step.id];
      const at = (why) => problems.push(`${step.id} (${guide.slug}): ${why}`);
      if (!r || r.status === "pending") {
        at("not walked");
        continue;
      }
      if (!STATUSES.includes(r.status)) {
        at(`status ${r.status} is not one of ${STATUSES.join(", ")}`);
        continue;
      }
      if (!r.observed?.trim()) at("nothing observed");
      const when = Date.parse(r.at);
      if (Number.isNaN(when)) at("no timestamp");
      else if (when < began) at("observed before the walk began");
      if (r.status === "fail") at("failed: the guide or REST Mail is wrong, so the walk is not complete until that is corrected and this step walked again");
      if (r.status === "substituted" && !r.reason?.trim()) at("substituted, and no reason");
      if (r.status === "pass" && !matches(step, r.observed, inst.vars)) at(`the observation does not show ${step.expect}`);
    }
    if (!guide.steps.some((s) => s.failure && receipt.steps?.[s.id]?.status === "pass")) problems.push(`${guide.slug}: no deliberate failure example was seen`);
  }
  for (const id of Object.keys(receipt.steps ?? {})) if (!spec.guides.some((g) => g.steps.some((s) => s.id === id))) problems.push(`${id}: not a step of the walk`);
  return { verdict: problems.length ? "NOT WALKED" : "WALKED", problems };
}

/** The tools a walk runs, with the versions it found; a missing tool is recorded as such, not skipped. */
function toolVersions() {
  const tools = {};
  for (const [name, args] of [["dig", ["-v"]], ["openssl", ["version"]], ["nc", ["-h"]], ["curl", ["--version"]], ["node", ["--version"]]]) {
    const r = spawnSync(name, args, { encoding: "utf8" });
    const first = `${r.stdout}${r.stderr}`.split("\n").find((l) => l.trim());
    tools[name] = r.error ? "not installed" : (first ?? "").trim();
  }
  return tools;
}

async function readSpec() {
  return JSON.parse(await readFile(SPEC, "utf8"));
}

async function main() {
  const { positionals, values } = parseArgs({
    options: {
      receipt: { type: "string" }, domain: { type: "string" }, host: { type: "string" }, ip: { type: "string" }, receiver: { type: "string" },
      "server-version": { type: "string" }, status: { type: "string" }, observed: { type: "string" }, evidence: { type: "string" }, correction: { type: "string" }, reason: { type: "string" },
    },
    allowPositionals: true,
  });
  const [command, id] = positionals;
  const spec = await readSpec();
  const root = git("rev-parse", "--show-toplevel");
  const now = new Date().toISOString();
  const load = async () => JSON.parse(await readFile(values.receipt, "utf8"));
  const save = (receipt) => writeFile(values.receipt, `${JSON.stringify(receipt, null, 2)}\n`);

  if (command === "plan") {
    console.log(`Installation: ${spec.installation.name} (${spec.installation.kind})\n${spec.installation.why}\n`);
    console.log(`Bring it up:\n  ${spec.installation.up.join("\n  ")}\nTake it down:\n  ${spec.installation.down.join("\n  ")}\n`);
    console.log(`Substitutions the receipt will carry:\n${spec.installation.substitutions.map((s) => `  - ${s}`).join("\n")}\n`);
    for (const g of spec.guides) {
      console.log(`${g.slug}  (${GUIDE}/${g.file})`);
      for (const s of g.steps) console.log(`  ${s.id}${s.failure ? " [deliberate failure]" : ""}  ${GUIDE}/${g.file}#${s.anchor}\n      do:      ${s.do}\n      observe: ${s.observe}${s.command ? `\n      probe:   ${s.command}` : ""}`);
    }
  } else if (command === "init") {
    if (!values.receipt || !values.domain || !values.host || !values.ip || !values.receiver) throw new Error("init needs --receipt --domain --host --ip --receiver");
    const versions = { server: values["server-version"] ?? "", guide_commit: git("-C", root, "rev-parse", "HEAD"), tools: toolVersions() };
    await save(newReceipt({ spec, vars: { domain: values.domain, host: values.host, ip: values.ip, receiver: values.receiver }, versions, now }));
    console.log(`${values.receipt}: ${spec.guides.flatMap((g) => g.steps).length} steps open. Record the server's version with --server-version (the API's /api/health says it).`);
  } else if (command === "probe") {
    const receipt = await load();
    const step = spec.guides.flatMap((g) => g.steps).find((s) => s.id === id);
    if (!step?.command) throw new Error(`${id} has no command to run: record it by hand`);
    const command = fill(step.command, receipt.installation.vars);
    const run = spawnSync("sh", ["-c", command], { encoding: "utf8", timeout: 60_000 });
    const observed = `$ ${command}\n${run.stdout}${run.stderr}`.trim();
    const status = matches(step, observed, receipt.installation.vars) ? "pass" : "fail";
    console.log(observed);
    recordStep(receipt, spec, id, status === "pass" ? { status, observed } : { status, observed, correction: "OPEN: the output is not what the guide says; compare, then correct the guide or the server and probe again" }, now);
    await save(receipt);
    console.log(`${id}: ${status}`);
    process.exitCode = status === "pass" ? 0 : 1;
  } else if (command === "record") {
    const receipt = await load();
    await save(recordStep(receipt, spec, id, { status: values.status, observed: values.observed, evidence: values.evidence, correction: values.correction, reason: values.reason }, now));
    console.log(`${id}: ${values.status}`);
  } else if (command === "check") {
    const receipt = await load();
    const { verdict, problems } = checkReceipt(receipt, spec, { guideCommit: git("-C", root, "rev-parse", "HEAD") });
    for (const p of problems) console.log(p);
    console.log(`${verdict}${problems.length ? `: ${problems.length} open` : ""}`);
    process.exitCode = verdict === "WALKED" ? 0 : 1;
  } else {
    console.error("usage: node walk.mjs plan | init | probe STEP | record STEP | check  (see the top of walk.mjs)");
    process.exitCode = 2;
  }
}

if (import.meta.url === `file://${process.argv[1]}`) await main();

import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { anchorsOf, checkGuide, checkOutside, checkOutsideLinks, EXCEPTIONS, linksOf, outsideLinks, readExceptions } from "./check.mjs";

const chapter = (body, front = "title: Open relay\npart: How it goes wrong\nsummary: How it happens.") => `---\n${front}\n---\n\n# Open relay\n\n${body}\n\n## Sources\n\n- [RFC 5321](https://www.rfc-editor.org/rfc/rfc5321)\n`;

const ARCHIFY = "3.2.0 208acba8d412335185b2056fedf54b3405df43fb";
const LOOK = "c".repeat(64);
const SOURCE = '{"meta":{"title":"Relay","visual_preset":"editorial"}}\n';
// A static image as svg.mjs exports it: stamped with its source, Archify and the look.
const image = (source = SOURCE, archify = ARCHIFY, look = LOOK) =>
  `<svg viewBox="0 0 10 10" data-source-sha256="${createHash("sha256").update(source).digest("hex")}" data-archify="${archify}" data-look-sha256="${look}"></svg>`;

// A small guide and repository to check: the front page, one chapter, one
// diagram, and some code to link to.
function check(pages, { files = {}, diagrams = ["relay.workflow.json", "relay.svg"] } = {}) {
  const all = new Map(Object.entries({ "README.md": "# Email, explained\n\n[Open relay](wrong/relay.md)\n", ...pages }));
  const tracked = {
    "internal/gateway/session.go": "package gateway\n\nfunc a() {}\n",
    "docs/MANUAL.md": "# Manual\n\n## Install\n",
    "docs/email-explained/diagrams/relay.workflow.json": SOURCE,
    "docs/email-explained/diagrams/relay.svg": image(),
    ...files,
  };
  const kinds = new Map();
  for (const path of Object.keys(tracked)) {
    kinds.set(path, "file");
    for (let dir = path.split("/").slice(0, -1).join("/"); dir; dir = dir.split("/").slice(0, -1).join("/")) kinds.set(dir, "directory");
  }
  for (const file of all.keys()) kinds.set(`docs/email-explained/${file}`, "file");
  const repo = { kind: (path) => kinds.get(path), text: (path) => tracked[path] };
  return checkGuide({ pages: all, repo, diagramFiles: new Set(diagrams), archify: ARCHIFY, look: LOOK }).map((p) => `${p.file}${p.line ? `:${p.line}` : ""}: ${p.message}`);
}

const good = chapter("![The relay](../diagrams/relay.svg)\n\nSee [the session](../../../internal/gateway/session.go#L3) and [installing](../../MANUAL.md#install).");

test("a sound guide has no problems", () => {
  assert.deepEqual(check({ "wrong/relay.md": good }), []);
});

test("links and anchors are GitHub's", () => {
  assert.deepEqual(
    linksOf("Text [a](b.md#c) and ![d](e.png).\n\n[f](https://x.example/g#h)").map((l) => [l.href, l.image, l.line]),
    [["b.md#c", false, 1], ["e.png", true, 1], ["https://x.example/g#h", false, 3]],
  );
  assert.deepEqual([...anchorsOf("# A\n\n## How common is it?\n\n## How common is it?\n\n## `mynetworks` that trusts")], ["a", "how-common-is-it", "how-common-is-it-1", "mynetworks-that-trusts"]);
});

test("a link to a page, a heading, a file or a line that is not there is a problem", () => {
  const found = check({
    "wrong/relay.md": chapter(
      [
        "![The relay](../diagrams/relay.svg)",
        "[gone](missing.md)",
        "[no heading](#nowhere)",
        "[front page heading](../README.md#nowhere)",
        "[code](../../../internal/gateway/nope.go)",
        "[past the end](../../../internal/gateway/session.go#L40)",
        "[manual heading](../../MANUAL.md#upgrading)",
        "[outside](../../../../etc/passwd)",
        "[scheme](ftp://example.org/x)",
      ].join("\n\n"),
    ),
  });
  assert.deepEqual(found, [
    "docs/email-explained/wrong/relay.md:11: missing.md does not exist",
    "docs/email-explained/wrong/relay.md:13: #nowhere is not a heading on this page",
    "docs/email-explained/wrong/relay.md:15: ../README.md#nowhere: #nowhere is not a heading there",
    "docs/email-explained/wrong/relay.md:17: ../../../internal/gateway/nope.go does not exist",
    "docs/email-explained/wrong/relay.md:19: ../../../internal/gateway/session.go#L40: line 40 is past the end (4 lines)",
    "docs/email-explained/wrong/relay.md:21: ../../MANUAL.md#upgrading: #upgrading is not a heading there",
    "docs/email-explained/wrong/relay.md:23: ../../../../etc/passwd leaves the repository",
    "docs/email-explained/wrong/relay.md:25: link to ftp://example.org/x: only http, https and mailto links leave the guide",
  ]);
});

test("front matter has what the website reads, and nothing else", () => {
  assert.deepEqual(check({ "wrong/relay.md": good.replace("summary: How it happens.\n", "") }), ["docs/email-explained/wrong/relay.md:1: front matter has no summary"]);
  assert.deepEqual(check({ "wrong/relay.md": good.replace("---\n\n# Open", "colour: red\n---\n\n# Open") }), ["docs/email-explained/wrong/relay.md:1: front matter key colour is not one the website reads"]);
  assert.deepEqual(check({ "wrong/relay.md": good.replace("title: Open relay", "title: Open relay: a story") }), ["docs/email-explained/wrong/relay.md:2: this value needs double quotes for GitHub to read it as YAML"]);
  assert.deepEqual(check({ "wrong/relay.md": `# Open relay\n\n![The relay](../diagrams/relay.svg)\n\n## Sources\n` }), ["docs/email-explained/wrong/relay.md:1: no front matter: a chapter needs title, part and summary"]);
});

test("every chapter has its Sources, unless its claims are sourced in the chapters it links", () => {
  const unsourced = good.replace(/## Sources[\s\S]*$/, "");
  assert.deepEqual(check({ "wrong/relay.md": unsourced }), ['docs/email-explained/wrong/relay.md: no "## Sources" section (or "sources: linked" for a page sourced in the chapters it links)']);
  assert.deepEqual(check({ "wrong/relay.md": unsourced.replace("summary: How it happens.", "summary: How it happens.\nsources: linked") }), []);
  assert.deepEqual(check({ "wrong/relay.md": unsourced.replace("summary: How it happens.", "summary: How it happens.\nsources: somewhere") }), [
    'docs/email-explained/wrong/relay.md:1: sources: somewhere: the only value is "linked"',
    'docs/email-explained/wrong/relay.md: no "## Sources" section (or "sources: linked" for a page sourced in the chapters it links)',
  ]);
});

test("every diagram is shown somewhere, has its static image, and exists when named", () => {
  const unshown = good.replace("![The relay](../diagrams/relay.svg)\n\n", "");
  assert.deepEqual(check({ "wrong/relay.md": unshown }), ["docs/email-explained/diagrams/relay.workflow.json: no chapter shows this diagram"]);
  assert.deepEqual(check({ "wrong/relay.md": unshown.replace("summary: How it happens.", "summary: How it happens.\ndiagram: ../diagrams/relay.workflow.json") }), []);
  assert.deepEqual(check({ "wrong/relay.md": good }, { diagrams: ["relay.workflow.json"] }), [
    "docs/email-explained/wrong/relay.md:9: image ../diagrams/relay.svg is not a diagram's static image",
    "docs/email-explained/diagrams/relay.workflow.json: no chapter shows this diagram",
    "docs/email-explained/diagrams/relay.workflow.json: no static image (relay.svg) beside it",
  ]);
  assert.deepEqual(check({ "wrong/relay.md": good.replace("summary: How it happens.", "summary: How it happens.\ndiagram: ../diagrams/gone.workflow.json") }), [
    "docs/email-explained/wrong/relay.md:1: front matter names a diagram that does not exist: ../diagrams/gone.workflow.json",
  ]);
});

test("a static image must be exported from its source as it is now, by the pinned Archify", () => {
  assert.deepEqual(check({ "wrong/relay.md": good }, { files: { "docs/email-explained/diagrams/relay.svg": image('{"title":"Old"}\n') } }), [
    "docs/email-explained/diagrams/relay.svg: was not exported from relay.workflow.json as it is now: run svg.mjs relay",
  ]);
  assert.deepEqual(check({ "wrong/relay.md": good }, { files: { "docs/email-explained/diagrams/relay.svg": image(SOURCE, "3.1.0 1c2b42d6") } }), [
    `docs/email-explained/diagrams/relay.svg: was drawn by Archify 3.1.0 1c2b42d6, not the pinned ${ARCHIFY}: run svg.mjs relay`,
  ]);
  assert.deepEqual(check({ "wrong/relay.md": good }, { files: { "docs/email-explained/diagrams/relay.svg": "<svg viewBox=\"0 0 1 1\"></svg>" } }), [
    "docs/email-explained/diagrams/relay.svg: was not exported from relay.workflow.json as it is now: run svg.mjs relay",
  ]);
  assert.deepEqual(check({ "wrong/relay.md": good }, { files: { "docs/email-explained/diagrams/relay.svg": image(SOURCE, ARCHIFY, "d".repeat(64)) } }), [
    "docs/email-explained/diagrams/relay.svg: was not drawn in the current look (look.mjs): run svg.mjs relay",
  ]);
});

test("every diagram is drawn in the site's look (#823)", () => {
  const classic = '{"meta":{"title":"Relay"}}\n';
  assert.deepEqual(check({ "wrong/relay.md": good }, { files: { "docs/email-explained/diagrams/relay.workflow.json": classic, "docs/email-explained/diagrams/relay.svg": image(classic) } }), [
    'docs/email-explained/diagrams/relay.workflow.json: is not drawn in the site\'s look: its meta.visual_preset must be "editorial" (look.mjs)',
  ]);
});

test("outside links are collected once each, with every place they are used", () => {
  const links = outsideLinks(new Map([["a.md", "[x](https://a.example/p#s)\n\n[y](https://a.example/p#s)"], ["b.md", "[z](https://a.example/p#t)"]]));
  assert.deepEqual([...links], [
    ["https://a.example/p#s", ["docs/email-explained/a.md:1", "docs/email-explained/a.md:3"]],
    ["https://a.example/p#t", ["docs/email-explained/b.md:1"]],
  ]);
});

test("outside checks distinguish refused requests from broken links without dropping citations", async (context) => {
  const requests = [];
  context.mock.method(globalThis, "fetch", async (url) => {
    requests.push(url);
    const status = Number(new URL(url).pathname.slice(1));
    return new Response('<h1 id="present">Source</h1>', { status, headers: { "content-type": "text/html" } });
  });
  const links = new Map();
  for (const status of [200, 401, 403, 429, 404, 410]) {
    links.set(`https://source.example/${status}#present`, ["docs/email-explained/a.md:1", "docs/email-explained/b.md:2"]);
    links.set(`https://source.example/${status}#missing`, ["docs/email-explained/c.md:3"]);
  }

  assert.deepEqual(await checkOutside(links, { concurrency: 1 }), {
    broken: [
      { href: "https://source.example/200#missing", why: "no #missing on the page", where: "docs/email-explained/c.md:3" },
      ...[404, 410].flatMap((status) => [
        { href: `https://source.example/${status}#present`, why: `HTTP ${status}`, where: "docs/email-explained/a.md:1, docs/email-explained/b.md:2" },
        { href: `https://source.example/${status}#missing`, why: `HTTP ${status}`, where: "docs/email-explained/c.md:3" },
      ]),
    ],
    refused: [401, 403, 429].map((status) => ({
      url: `https://source.example/${status}`,
      status,
      where: "docs/email-explained/a.md:1, docs/email-explained/b.md:2, docs/email-explained/c.md:3",
    })),
    pages: 6,
  });
  assert.deepEqual(requests, [200, 401, 403, 429, 404, 410].map((status) => `https://source.example/${status}`));
});

// The outside-link check's verdict (#2120, AC02b). It fails on a broken link, a
// refusal (401, 403, 429) no exception covers, an exception without a reason, and
// an expired exception; a refusal passes only through a dated, reasoned exception.
// Each case answers through a stand-in fetch, so it needs no network; the live
// tests at the end ask real sites.

const TODAY = "2026-11-20";
const WHERE = "docs/email-explained/guides/why-mail-goes-to-spam.md:44";

/** checkOutsideLinks for one link, which answers every request with status. */
async function verdict(context, status, exceptions) {
  context.mock.method(globalThis, "fetch", async () => new Response("<p>page</p>", { status, headers: { "content-type": "text/html" } }));
  return checkOutsideLinks(new Map([["https://lookup.example/", [WHERE]]]), exceptions, TODAY, { concurrency: 1 });
}

const sound = { url: "https://lookup.example/", reason: "The lookup page turns away robots; a person opening it gets the lookup form.", expires: "2027-02-20" };

test("outside check: a broken link fails", async (context) => {
  const { failures } = await verdict(context, 404, []);
  assert.deepEqual(failures, [`broken: https://lookup.example/: HTTP 404  (${WHERE})`]);
});

test("outside check: a broken link fails even with an exception, which covers only refusals", async (context) => {
  const { failures, excused } = await verdict(context, 410, [sound]);
  assert.deepEqual(failures, [`broken: https://lookup.example/: HTTP 410  (${WHERE})`]);
  assert.deepEqual(excused, []);
});

test("outside check: a refusal with no exception fails", async (context) => {
  for (const status of [401, 403, 429]) {
    const { failures } = await verdict(context, status, []);
    assert.deepEqual(failures, [`refused to be checked (HTTP ${status}), with no exception: https://lookup.example/  (${WHERE})`], `HTTP ${status}`);
  }
  // An exception for another page does not cover this one.
  const { failures } = await verdict(context, 403, [{ ...sound, url: "https://lookup.example/other" }]);
  assert.deepEqual(failures, [`refused to be checked (HTTP 403), with no exception: https://lookup.example/  (${WHERE})`]);
});

test("outside check: an exception without a reason fails, and does not excuse the refusal", async (context) => {
  for (const reason of [undefined, "", "   "]) {
    const { failures, excused } = await verdict(context, 403, [{ url: sound.url, expires: sound.expires, ...(reason === undefined ? {} : { reason }) }]);
    assert.deepEqual(failures, [
      "exception https://lookup.example/: has no reason: say why the link is sound although the site refuses to be checked",
      `refused to be checked (HTTP 403), with no exception: https://lookup.example/  (${WHERE})`,
    ], `reason ${JSON.stringify(reason)}`);
    assert.deepEqual(excused, []);
  }
});

test("outside check: an expired exception fails, and does not excuse the refusal", async (context) => {
  const { failures, excused } = await verdict(context, 429, [{ ...sound, expires: "2026-11-19" }]);
  assert.deepEqual(failures, [
    "exception https://lookup.example/: expired on 2026-11-19: open the link, and set a new date if it is still sound, or replace it",
    `refused to be checked (HTTP 429), with no exception: https://lookup.example/  (${WHERE})`,
  ]);
  assert.deepEqual(excused, []);
  // An expired exception fails even while its link answers: it has to be renewed or removed.
  const answering = await verdict(context, 200, [{ ...sound, expires: "2026-01-01" }]);
  assert.deepEqual(answering.failures, ["exception https://lookup.example/: expired on 2026-01-01: open the link, and set a new date if it is still sound, or replace it"]);
});

test("outside check: an exception with no date, or not a date, fails", async (context) => {
  for (const expires of [undefined, "", "soon", "2027-02-30", "20/02/2027"]) {
    const { failures } = await verdict(context, 403, [{ url: sound.url, reason: sound.reason, ...(expires === undefined ? {} : { expires }) }]);
    assert.equal(failures[0], "exception https://lookup.example/: has no expiry date: expires must be a date, YYYY-MM-DD", `expires ${JSON.stringify(expires)}`);
    assert.equal(failures.length, 2);
  }
});

test("outside check: a refusal passes through a dated, reasoned exception, up to the day it expires", async (context) => {
  for (const expires of [sound.expires, TODAY]) {
    const { failures, excused } = await verdict(context, 403, [{ ...sound, expires }]);
    assert.deepEqual(failures, [], `expires ${expires}`);
    assert.deepEqual(excused, [`excepted until ${expires}, refused to be checked (HTTP 403): https://lookup.example/: ${sound.reason}  (${WHERE})`]);
  }
});

test("outside check: a link that answers passes with no exception", async (context) => {
  const { failures, excused } = await verdict(context, 200, []);
  assert.deepEqual(failures, []);
  assert.deepEqual(excused, []);
});

test("exceptions file: one entry per page, only its three keys, and a readable file", () => {
  assert.deepEqual(readExceptions([sound, sound], TODAY).problems, ["https://lookup.example/: is listed twice"]);
  assert.deepEqual(readExceptions([{ ...sound, owner: "me" }], TODAY).problems, ["https://lookup.example/: has a key owner: an exception has only url, reason and expires"]);
  assert.deepEqual(readExceptions([{ ...sound, url: "https://lookup.example/#form" }], TODAY).problems, ["https://lookup.example/#form: has no url, or one that is not an http(s) page without an anchor"]);
  assert.deepEqual(readExceptions({ exceptions: [] }, TODAY).problems, ["file must be a JSON array of exceptions, each with url, reason and expires"]);
  assert.deepEqual(readExceptions({ unreadable: "x.json: Unexpected token" }, TODAY).problems, ["file cannot be read: x.json: Unexpected token"]);
  assert.deepEqual([...readExceptions([sound], TODAY).valid.keys()], [sound.url]);
});

// Its expiry dates are judged on a fixed day here, so a pull request never fails
// because time passed; the scheduled check judges them on the day it runs.
test("the checked-in exceptions file is well formed", async () => {
  const { readFile } = await import("node:fs/promises");
  const entries = JSON.parse(await readFile(EXCEPTIONS, "utf8"));
  assert.deepEqual(readExceptions(entries, TODAY).problems, []);
});

// The command itself, with every request answered by a stand-in fetch loaded
// before it runs: it exits 1 when a refusal has no exception and 0 when a dated,
// reasoned exception covers every refusal. No request leaves the machine.
test("node check.mjs --external exits 1 on an unlisted refusal and 0 when an exception covers it", async () => {
  const dir = await mkdtemp(join(tmpdir(), "outside-check-"));
  try {
    const stub = join(dir, "refuse.mjs");
    // Not HTML, so the anchors in the pages that answer are not looked for.
    await writeFile(stub, 'globalThis.fetch = async (url) => new Response("no robots", { status: String(url).startsWith("https://www.rfc-editor.org/") ? 403 : 200, headers: { "content-type": "text/plain" } });\n');
    const run = async (exceptions) => {
      const file = join(dir, "exceptions.json");
      await writeFile(file, JSON.stringify(exceptions));
      const script = fileURLToPath(new URL("check.mjs", import.meta.url));
      try {
        const { stdout } = await promisify(execFile)(process.execPath, ["--import", stub, script, "--external", "--exceptions", file], { maxBuffer: 1 << 24 });
        return { code: 0, stdout };
      } catch (error) {
        return { code: error.code, stdout: error.stdout };
      }
    };
    const refusedPages = (stdout) => [...stdout.matchAll(/^refused to be checked \(HTTP 403\), with no exception: (\S+)/gm)].map((m) => m[1]);

    const unlisted = await run([]);
    assert.equal(unlisted.code, 1, unlisted.stdout);
    const pages = refusedPages(unlisted.stdout);
    assert.ok(pages.length > 0, `the guide cites no rfc-editor.org page to refuse:\n${unlisted.stdout}`);

    const far = new Date(Date.now() + 30 * 86400_000).toISOString().slice(0, 10);
    const listed = await run(pages.map((url) => ({ url, reason: "Stand-in refusal for this test.", expires: far })));
    assert.equal(listed.code, 0, listed.stdout);
    assert.match(listed.stdout, new RegExp(`refused to be checked \\(${pages.length} by exception`));
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

// Live: real sites, asked only when EMAIL_EXPLAINED_LIVE_LINKS=1, which the
// scheduled workflow sets. Without it (a pull request, a laptop offline) these
// two are skipped and say so; with it they run and fail when the network is
// down, as a broken link would.
const live = process.env.EMAIL_EXPLAINED_LIVE_LINKS === "1" ? {} : { skip: "set EMAIL_EXPLAINED_LIVE_LINKS=1 to ask real sites" };

test("live: a page that is there passes the outside check", live, async () => {
  const links = new Map([["https://www.rfc-editor.org/rfc/rfc5321", ["live-test"]]]);
  const { failures, broken, refused } = await checkOutsideLinks(links, [], new Date().toISOString().slice(0, 10), { concurrency: 1 });
  assert.deepEqual({ failures, broken, refused }, { failures: [], broken: [], refused: [] });
});

test("live: a page that is not there fails the outside check", live, async () => {
  const links = new Map([["https://www.rfc-editor.org/rfc/rfc5321-no-such-page", ["live-test"]]]);
  const { failures } = await checkOutsideLinks(links, [], new Date().toISOString().slice(0, 10), { concurrency: 1 });
  assert.deepEqual(failures, ["broken: https://www.rfc-editor.org/rfc/rfc5321-no-such-page: HTTP 404  (live-test)"]);
});

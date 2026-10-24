import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { test } from "node:test";
import { anchorsOf, checkGuide, linksOf, outsideLinks } from "./check.mjs";

const chapter = (body, front = "title: Open relay\npart: How it goes wrong\nsummary: How it happens.") => `---\n${front}\n---\n\n# Open relay\n\n${body}\n\n## Sources\n\n- [RFC 5321](https://www.rfc-editor.org/rfc/rfc5321)\n`;

const ARCHIFY = "3.2.0 208acba8d412335185b2056fedf54b3405df43fb";
const SOURCE = '{"title":"Relay"}\n';
// A static image as svg.mjs exports it: stamped with its source and Archify.
const image = (source = SOURCE, archify = ARCHIFY) =>
  `<svg viewBox="0 0 10 10" data-source-sha256="${createHash("sha256").update(source).digest("hex")}" data-archify="${archify}"></svg>`;

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
  return checkGuide({ pages: all, repo, diagramFiles: new Set(diagrams), archify: ARCHIFY }).map((p) => `${p.file}${p.line ? `:${p.line}` : ""}: ${p.message}`);
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
});

test("outside links are collected once each, with every place they are used", () => {
  const links = outsideLinks(new Map([["a.md", "[x](https://a.example/p#s)\n\n[y](https://a.example/p#s)"], ["b.md", "[z](https://a.example/p#t)"]]));
  assert.deepEqual([...links], [
    ["https://a.example/p#s", ["docs/email-explained/a.md:1", "docs/email-explained/a.md:3"]],
    ["https://a.example/p#t", ["docs/email-explained/b.md:1"]],
  ]);
});

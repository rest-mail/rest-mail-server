import assert from "node:assert/strict";
import { test } from "node:test";
import { anchorer, compileChapter, extractFonts, readingOrder, slugOf, splitFrontMatter } from "./bundle.mjs";

const commit = "0123456789abcdef0123456789abcdef01234567";
const files = new Set(["README.md", "how-it-goes-wrong/open-relay.md"]);
const diagrams = new Map([["open-relay", {}]]);
const compile = (text, file = "how-it-goes-wrong/open-relay.md") => compileChapter({ file, text, commit, files, diagrams });

test("anchors are GitHub's, numbered when a heading repeats", () => {
  const anchor = anchorer();
  assert.equal(anchor("1. mynetworks that trusts too much"), "1-mynetworks-that-trusts-too-much");
  assert.equal(anchor("How common is it?"), "how-common-is-it");
  assert.equal(anchor("How common is it?"), "how-common-is-it-1");
});

test("a chapter's address is its path, and a README is its folder's", () => {
  assert.equal(slugOf("README.md"), "");
  assert.equal(slugOf("how-it-goes-wrong/open-relay.md"), "how-it-goes-wrong/open-relay");
  assert.equal(slugOf("protocols/README.md"), "protocols");
});

test("front matter is read, and a line it does not understand is an error", () => {
  assert.deepEqual(splitFrontMatter("---\ntitle: A\npart: B\n---\nbody").meta, { title: "A", part: "B" });
  assert.throws(() => splitFrontMatter("---\n- nope\n---\n"), /not understood/);
  assert.equal(splitFrontMatter('---\ntitle: "Reading mail: IMAP and \\"JMAP\\""\n---\n').meta.title, 'Reading mail: IMAP and "JMAP"');
});

test("sections split at ##, the # title is dropped, links and diagrams are rewritten", () => {
  const chapter = compile(`---
title: The open relay
part: How it goes wrong
diagram: ../diagrams/open-relay.workflow.json
---

# The open relay

Intro, see [the front page](../README.md#contents).

![The relay](../diagrams/open-relay.svg)

## How common is it?

Code is [here](../../../internal/gateway/smtp/session.go#L10), the rules [there](../WRITING.md).
`);
  assert.equal(chapter.title, "The open relay");
  assert.equal(chapter.diagram, "open-relay");
  assert.match(chapter.intro, /href="\/email-explained#contents"/);
  assert.match(chapter.intro, /^<figure class="guide-diagram" data-diagram="open-relay"><img src="diagrams\/open-relay.svg" alt="The relay"><\/figure>$/m);
  assert.doesNotMatch(chapter.intro, /<h1|<p><figure/);
  assert.deepEqual(chapter.sections.map((s) => s.id), ["how-common-is-it"]);
  const html = chapter.sections[0].html;
  assert.match(html, new RegExp(`https://github.com/rest-mail/rest-mail-server/blob/${commit}/internal/gateway/smtp/session.go#L10`));
  // Not a chapter of the guide, so it stays a file on GitHub.
  assert.match(html, new RegExp(`blob/${commit}/docs/email-explained/WRITING.md`));
});

test("an image that is not a diagram, or a diagram that does not exist, fails the build", () => {
  assert.throws(() => compile("# T\n\n![x](photo.png)"), /only diagram images/);
  assert.throws(() => compile("---\ntitle: T\ndiagram: ../diagrams/missing.workflow.json\n---\n"), /missing diagram/);
});

test("typefaces carried as data: URLs become files named for their contents", () => {
  const data = Buffer.from("not really a font").toString("base64");
  const { html, fonts } = extractFonts(`@font-face { src: url(data:font/woff2;base64,${data}) format('woff2'); }`);
  const [file] = fonts.keys();
  assert.match(file, /^fonts\/[0-9a-f]{16}\.woff2$/);
  assert.equal(html, `@font-face { src: url(${file}) format('woff2'); }`);
  assert.equal(fonts.get(file).toString(), "not really a font");
});

test("chapters come in the order the front page lists them, then the rest by path", () => {
  const files = ["README.md", "a/second.md", "a/unlisted.md", "b/first.md"];
  const readme = "- [First](b/first.md)\n- [Second](a/second.md#part)\n- [First again](b/first.md)\n- [Elsewhere](../MANUAL.md)";
  assert.deepEqual(readingOrder(readme, files), ["README.md", "b/first.md", "a/second.md", "a/unlisted.md"]);
});

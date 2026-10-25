import assert from "node:assert/strict";
import { test } from "node:test";
import { LOOK, withLook } from "./look.mjs";
import { dropUnusedFonts, finish, provenanceOf } from "./svg.mjs";

const face = (range, data) => `@font-face { font-family: 'JetBrains Mono'; src: url(data:font/woff2;base64,${data}) format('woff2'); unicode-range: ${range}; }\n`;
const exported = `<?xml version="1.0"?>\n<svg viewBox="0 0 1200 700" width="1200" height="700" style="width: 1200px; height: 700px; min-width: 0px;" xmlns="http://www.w3.org/2000/svg"><style>${face("U+0400-045F, U+0490-0491", "CYRILLIC")}${face("U+0000-00FF, U+2000-206F", "LATIN")}</style><text>Spammer’s bot</text></svg>`;

test("an image keeps only the typeface subsets whose characters it shows", () => {
  const svg = dropUnusedFonts(exported);
  assert.match(svg, /LATIN/);
  assert.doesNotMatch(svg, /CYRILLIC/);
  assert.match(dropUnusedFonts(exported.replace("Spammer’s bot", "Почта")), /CYRILLIC/);
});

test("the export is cropped to the drawing and records where it came from", () => {
  const svg = finish(exported, { x: 0, y: 3, width: 1152, height: 604 }, { source: "a".repeat(64), archify: "3.2.0 208acba8", look: "b".repeat(64) });
  const root = svg.match(/<svg\b[^>]*>/)[0];
  assert.match(root, / viewBox="0 3 1152 604"/);
  assert.match(root, / width="1152" height="604"/);
  assert.doesNotMatch(root, /style=/);
  assert.deepEqual(provenanceOf(svg), { source: "a".repeat(64), archify: "3.2.0 208acba8", look: "b".repeat(64) });
});

test("a diagram's page takes the look after Archify's own styles, so the look wins", () => {
  const page = withLook("<html><head><style>[data-theme=\"light\"] { --bg: #f4f5f7; }</style></head><body></body></html>");
  assert.ok(page.indexOf(LOOK) > page.indexOf("--bg: #f4f5f7"));
  assert.ok(page.indexOf(LOOK) < page.indexOf("</head>"));
  assert.throws(() => withLook("<svg></svg>"), /no <\/head>/);
});

// The diagrams' one look, on rest-mail.com and in the SVGs GitHub shows
// (#823): Archify's editorial preset, which every source names in
// meta.visual_preset (check.mjs holds them to it), with its paper, ink and
// emphasis set to the site's live design, Safe (rest-mail/website:
// src/styles/shared.css and src/designs/Safe.tsx). Its typeface stays
// Archify's JetBrains Mono, which is Safe's own monospace.
//
// Safe has no dark design; its dark sections set the same ink as their
// background, with light paper-coloured text and the same cobalt.
import { createHash } from "node:crypto";

export const PRESET = "editorial";

export const LOOK = `[data-preset="editorial"][data-theme="light"] {
  --bg: #f4f1ea;
  --text: #15110d;
  --text-muted: #5a5247;
  --text-faint: #5a5247;
  --text-dim: #8a8175;
  --arrow-emphasis: #1840ff;
}
[data-preset="editorial"][data-theme="dark"] {
  --bg: #15110d;
  --text: #e8e3d6;
  --arrow-emphasis: #1840ff;
}
`;

/** What an image records of the look it was drawn in. */
export const lookHash = createHash("sha256").update(LOOK).digest("hex");

/** Archify's page for a diagram, in the look: after its own styles, so it wins. */
export function withLook(html) {
  if (!html.includes("</head>")) throw new Error("Archify's page has no </head> to put the look before");
  return html.replace("</head>", `<style data-look="rest-mail">\n${LOOK}</style>\n</head>`);
}

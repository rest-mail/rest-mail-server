# Desktop appearance

Open **REST Mail → Settings…** with **⌘,**, or use the paintbrush in the title bar.
Appearance is available before sign-in as well. Changes apply immediately; there
is no Apply button and no page reload. Mail and draft tabs remain mounted behind
Settings. Escape or **Back to app** returns to exactly where you were.

## The collection

| Theme | Character | Sidebar / paper / accent | Display / UI / reading / mono |
| --- | --- | --- | --- |
| REST Mail | Confident navy and sunshine | `#1a2a4a` / `#ffffff` / `#ffcc00` | Bricolage Grotesque / Instrument Sans / Instrument Sans / Geist Mono |
| Letterpress | Warm editorial paper | `#3a2d27` / `#fffaf0` / `#e9b65f` | DM Serif Display / Instrument Sans / Newsreader / Parcel Mono |
| Phosphor | Quiet terminal green | `#08140f` / `#10231b` / `#b8fa74` | Space Grotesk / Parcel Mono / Parcel Sans / JetBrains Mono |
| Clarity | High-contrast, generous legibility | `#000000` / `#ffffff` / `#ffe500` | Atkinson Hyperlegible / Atkinson Hyperlegible / Literata / Parcel Mono |
| Sorbet | Lilac, rose, and soft corners | `#efe2f5` / `#fffbff` / `#efb9c9` | Fraunces / Manrope / Geist / Geist Mono |
| Midnight | Ink blue and electric lavender | `#0c1125` / `#182039` / `#b7b2ff` | Space Grotesk / Geist / Newsreader / Geist Mono |
| Concrete | Hard edges and heavy type | `#f5f330` / `#ffffff` / `#f5f330` | Unbounded / Archivo / Roboto Slab / Parcel Mono |
| System Seven | A pixel of classic Mac nostalgia | `#deded8` / `#fffef8` / `#b5d6f6` | Silkscreen / Geist / Parcel Sans / Parcel Mono |
| Canopy | Deep forest, relaxed rhythm | `#0d201c` / `#1c3329` / `#d4e78d` | Fraunces / Manrope / Literata / JetBrains Mono |
| Afterglow | Apricot light and plum shadows | `#341c38` / `#fff8f3` / `#ffc187` | Syne / Space Grotesk / Parcel Serif / Geist Mono |

Every preset supplies its own palette and four-role type pairing. Parcel fonts
are renamed Latin subsets of IBM Plex; see `FONTS.md` for licenses and provenance.
HTML messages remain sandboxed, with the original content untouched. Desktop
reader chrome follows the selected theme; a light paper body gives HTML content
a predictable canvas on both light and dark themes. Its reading face is embedded
from bundled font data, within the existing `font-src data:` policy. Scripts,
remote-font loading and sandbox permissions are not enabled by appearance.

## Remix studio

Pick themes A and B, then choose colours, typography, and shape from either.
**Surprise me** guarantees different colour and type sources. The accent picker
automatically chooses contrasting button text; buttons also have a contrasting
edge. Override each font role, 0–24px corner radius, and 28–48px row spacing.
Edits persist as **Custom**. Give a combination a name to keep a reusable snapshot
under **Your themes**. Deleting a saved snapshot leaves the current visual result
as Custom. **Reset to theme** restores the current colour preset's original
typefaces, shape, and accent, clearing all overrides and remix sources.

Settings use desktop-only localStorage keys, separately scoped by server
host and case-normalized signed-in email. Sign-out preserves the visual treatment
for the sign-in screen, but hides personal saved themes and switches writes to a
guest preference scope. Storage corruption falls back safely to REST Mail. Custom
names are plain text, and style overrides are restricted to the bundled fonts,
six-digit hex colours, and bounded numeric values.

## Agent tools

- `ui_theme_list`: built-in themes with palette/font/shape tokens, plus saved remixes.
- `ui_theme_get`: active name, selected ID, remix specification, and resolved tokens.
- `ui_theme_set`: supply exactly one `theme` name/ID or `remix` specification.

For example:

```json
{"theme":"Midnight"}
```

```json
{"remix":{"colours":"Canopy","typefaces":"Letterpress","shape":"Sorbet","overrides":{"accent":"#ffcc99","display":"Syne","radius":18,"density":40}}}
```

`ui_navigate` accepts `/settings/appearance`; `ui_menu` accepts `settings` even
before sign-in. These tools use the authenticated, existing desktop MCP bridge;
they never change mail-server settings or shared webmail preferences.

## Contrast contract

`contrast.test.ts` computes WCAG relative luminance from the actual preset
tokens: body, muted, danger, success, link/action, sidebar, selection and badge
text must reach 4.5:1. Control boundaries, unread indicators, and sign-in braces
must reach 3:1. Accent text uses the higher-contrast light/dark ink, and every
accent control has a matching contrasting boundary. All 1,000 palette/type/shape
combinations are tested for category isolation. Arbitrary user overrides do not
change body/surface colours; they cannot lower the preset body-text contrast.
The accent override's foreground is recomputed automatically.

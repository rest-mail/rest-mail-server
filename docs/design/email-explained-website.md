# Email, explained on the website: design

Status: discussion. Nothing here is built yet. Each point is marked
**Decided**, **Leaning** or **Open**; only Decided points get built.

[Email, explained](../email-explained/README.md) is written as Markdown in this
repository, next to the code it describes. The same guide has to appear on
[rest-mail.com](https://rest-mail.com) as richer pages: interactive diagrams,
the site's own design, navigation between chapters. This document is about how
one set of files becomes both, without the two ever drifting apart.

## What exists today

- **The guide:** `docs/email-explained/`. Each chapter is a Markdown file with
  front matter (`title`, `part`, `summary`, `diagram`). Diagrams are Archify
  JSON sources in `diagrams/`, with a committed static image for GitHub
  readers. The rules are in [WRITING.md](../email-explained/WRITING.md).
- **The website:** [rest-mail/website](https://github.com/rest-mail/website),
  built with TanStack Start (React, rendered on the server). Pages are routes in
  `src/routes/`, written as components: the About page is a `<Doc>` of
  `<Section>`s. The site has three designs (Safe, Bold, Experimental), and each
  one draws `Doc` and `Section` its own way.

## Decided

- **The Markdown is the source of truth** (2026-10-23, owner). Nobody edits the
  guide's text in the website repository. A change to a chapter is a change to
  this repository, reviewed with the code it describes.
- **The website is built from it mechanically** (2026-10-23, owner). When the
  guide changes, the site updates without anyone copying text across.
- **The files stay readable on GitHub.** Relative links work there, diagrams
  have a static image, and no website-only syntax makes a chapter unreadable as
  plain Markdown.
- **Diagrams are made with Archify** (2026-10-23, owner). A side-by-side with
  diagram-design showed diagram-design's static editorial style reads calmer,
  but Archify carries the whole technical story, every mistake wired to where it
  takes effect, and readers can explore it. That suits a technical guide to why
  REST Mail exists. Its JSON sources and automatic layout also suit a mechanical
  build better than coordinates placed by hand.
- **The guide is checked in CI** (2026-10-24, owner). Every pull request runs
  `scripts/email-explained/check.mjs` as the `Email, explained` job, part of
  the required `CI` check: links and anchors inside the repository, front
  matter, a Sources section in every chapter (or `sources: linked` for a page
  sourced in the chapters it links), no orphaned diagram, Archify's validation
  of every source, and a bundle that builds. Outside links and their anchors are
  checked daily (`scheduled.yml`); a broken one opens an issue that closes
  itself once it is fixed. Archify is fetched at the commit
  `scripts/email-explained/package.json` pins, the version the diagrams were
  made with. Each static image records the hash of the source it was exported
  from, and CI fails when they differ.
- **The static image is an SVG** (2026-10-24, owner). Archify's own export:
  sharp at any size, the typeface inside it (only the subsets its text uses),
  the reader's light or dark theme, cropped to the drawing.
  `scripts/email-explained/svg.mjs` makes them all in one headless Chrome. The
  26 come to 3.3 MB, against 3.5 MB as PNGs.
- **The website publishes the API reference too** (2026-10-23, owner). Once
  the server serves its generated `openapi.json` (#623), the same pipeline
  turns it into reference pages on rest-mail.com, so the reference is as
  current as the code.

## Leaning

### The repository that owns the words builds them

Two ways to connect the repositories:

1. **The website fetches the Markdown** at build time and compiles it itself.
   This is simple to start, but the website's build then needs Archify, the
   diagram checks and this repository's layout. A broken chapter is discovered
   in the other repository, by whoever happens to be building the site.
2. **This repository builds a content bundle**, and the website consumes it.
   CI here compiles the guide on every change under `docs/email-explained/`. It
   produces, for each chapter, its HTML fragments by section, its metadata, its
   rendered diagrams (interactive HTML and a static SVG) and its outbound links,
   all pinned to the commit. It publishes them as a versioned artifact.
   The website pins a bundle version and renders it with its own components. A
   new bundle arrives as a dependency bump, which merges on green, the way #628
   handles go-oapifly and Vertrag.

Leaning: **2**. The checks run where the writing happens, so a broken link or
an unsourced chapter fails the PR that broke it. The website stays a renderer
and never learns how the guide is made. It is also the same shape as the
contract work, where this repository publishes `openapi.json` and its clients
consume it (#626, #663).

### Plain Markdown, with a few conventions, not MDX

MDX would let a chapter embed React components, but GitHub shows MDX as broken
Markdown, which breaks the third Decided point. Instead, a chapter uses plain
Markdown, and the build recognises a few conventions:

- **Front matter** becomes the page's title, description, part and order.
- **`##` headings** become `<Section>`s, so each design draws a chapter in its
  own style. Their anchors are the section ids other pages can refer to.
- **A diagram image** that points at `diagrams/<name>.svg`, next to its
  `<name>.<type>.json` source, becomes the interactive diagram on the website.
  On GitHub it stays the static image.
- **Links between chapters** become site routes. **Links to code**
  (`../../../internal/...`) become GitHub links pinned to the bundle's commit,
  so they never point at code that has since moved.

### Pages can reuse a section

Another page, such as About or a landing page, can pull in one section of a
chapter by its anchor, for example
`how-it-goes-wrong/open-relay-and-spam-machine#how-common-is-it`, instead of
repeating its text. The build fails if that section no longer exists, so a page
can never quote a paragraph that has gone.

## Open

1. **How interactive diagrams sit in the page.** To be decided by looking at a
   working prototype (owner, 2026-10-23). The prototype is the website's
   `feat/email-explained-prototype` branch: every chapter takes `?embed=` to
   switch between six ways, on the real site with its real
   Content-Security-Policy. What each one did, in headless Chrome, 2026-10-23:

   | `?embed=` | Result | Why |
   |---|---|---|
   | `image` | Works, static | The static image GitHub shows (a PNG then, an SVG since). Nothing to explore. |
   | `iframe` | Blocked, then works | The site sends `frame-ancestors 'none'` on every response, and refuses Archify's `data:` typefaces. Served with a policy of their own (`frame-ancestors 'self'`, `font-src data:`), diagrams load, and the page sizes the frame to fit because it is the same origin. |
   | `iframe-embed` | Works | The same, in Archify's `?embed=1` mode: only the diagram, without the title, toolbar, cards and node index that repeat the chapter. |
   | `srcdoc` | Works, with a fixed height | Archify's own advice. An opaque sandboxed frame cannot be measured, so it scrolls inside a fixed box. |
   | `shadow` | Broken | Archify's stylesheet is written for a whole document (`:root`, `html[...]`), so inside a shadow root none of its colours resolve and every box is black. Its script looks its elements up in `document`, finds none, and throws. The page's typefaces still inherit in. |
   | `inline` | Renders, but the site leaks in | Its CSS scoped to the diagram, so its variables cannot reach the site. The other way still leaks: the site's own rules (`.doc-body li`) match Archify's elements and make its cards unreadable. The site's policy also refuses its typefaces, and its script writes 12 attributes and a style onto the site's `<html>`. |
   | `inline-ns` | Works, with one diagram | Everything Archify names is prefixed or redirected (below). It looks and behaves like the frame, the site's `<html>`, anchors and typefaces are untouched, and nothing is refused. |
   | `inline-ns-x2` | Two diagrams mostly independent | Each has its own state, theme and address. They still share element ids: the second's arrowheads are drawn with the first's markers (the wrong colour when their themes differ), and its accessible name points at the first's title. |

   **Namespacing, collision by collision** (`inline-ns`, the website's
   `src/guide/namespaced.ts`):

   | Collision | Namespaced by |
   |---|---|
   | The site's rules and inherited styles reach Archify's elements | The diagram's element is reset to the browser's defaults (`all: initial`, its descendants `all: revert`), and every Archify rule is prefixed with the element's id, which outranks the site's classes |
   | Archify's rules for `html`, `:root`, `body` and bare `[data-theme]` | Rewritten to the element itself |
   | Its script writes its state onto `<html>` | It is given a document whose `documentElement`, `body` and lookups are the element |
   | Both define a typeface called JetBrains Mono | Archify's is renamed |
   | The site's policy refuses Archify's `data:` typefaces | The bundle writes them out as files beside the diagram |
   | Archify keeps its state after `#`, and clears it, overwriting the site's anchors | Its script sees an address of its own, kept in `?<diagram>=` (`?open-relay=focus%3Drelay`); a link to that address opens the diagram focused. Archify's `data-reading-link-template` makes its "copy link" give the same |
   | Absolutely placed parts (its toolbar) anchor to the site's figure | The element is their positioning context |
   | Element ids repeat when a diagram appears twice | **Not solved.** Prefixing the ids would change values Archify's script reads back; it needs an id prefix in Archify itself |
   | Its saved theme (`localStorage` `archify-theme`) | Left shared on purpose: a reader's choice applies to every diagram |

   So namespacing does avoid the collisions, all but repeated ids, at the
   cost of a rewriting layer between Archify and the page that has to follow
   Archify's releases. A frame needs none of it.

2. **Matching the site's three designs.** Archify has its own looks (classic,
   editorial and others). Does a diagram follow the visitor's chosen design,
   or keep one look everywhere? To be decided by seeing it, in the same
   prototype (owner, 2026-10-23).
3. **Where the bundle is published:** a GitHub release asset, an OCI artifact
   next to the images, or a branch the website reads.
4. **What the website adds of its own:** navigation between chapters, a
   contents page, search, "last updated from commit …" on every page. These
   live in the website repository and are not part of the bundle.
5. **Other repositories' documentation.** The guide may later split, with
   protocol chapters moving to their libraries' repositories. The bundle format
   should not assume one repository, so the website can merge several bundles
   later.

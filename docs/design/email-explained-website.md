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
- **A diagram image** that points at `diagrams/<name>.png`, next to its
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

### Checks, in CI, on every PR that touches the guide

- Every chapter's front matter matches its schema.
- Every internal link and anchor resolves. External links and anchors are
  checked daily, since other sites change on their own schedule (#626's daily
  tier).
- Every chapter has a Sources section, as `WRITING.md` requires.
- Every diagram source passes Archify's `finalize`, and its committed static
  image matches what the source renders.
- No diagram is orphaned, and no chapter links a diagram that does not exist.

## Open

1. **How interactive diagrams sit in the page.** To be decided by looking at a
   working prototype of both (owner, 2026-10-23). Options:
   - an `<iframe>` of Archify's standalone HTML: isolated and simple, but
     its own scroll and theme;
   - inlining its SVG and viewer runtime into the page: seamless, but the
     site's styles and Archify's must not collide.

2. **Matching the site's three designs.** Archify has its own looks (classic,
   editorial and others). Does a diagram follow the visitor's chosen design,
   or keep one look everywhere? To be decided by seeing it, in the same
   prototype (owner, 2026-10-23).
3. **The static fallback for GitHub.** Today a PNG, cropped from a screenshot.
   Archify can export SVG that follows the reader's light or dark mode, which
   is sharper and smaller. That export needs automating (a headless browser
   driving the export, or a renderer option).
4. **Where the bundle is published:** a GitHub release asset, an OCI artifact
   next to the images, or a branch the website reads.
5. **What the website adds of its own:** navigation between chapters, a
   contents page, search, "last updated from commit …" on every page. These
   live in the website repository and are not part of the bundle.
6. **Other repositories' documentation.** The guide may later split, with
   protocol chapters moving to their libraries' repositories. The bundle format
   should not assume one repository, so the website can merge several bundles
   later.

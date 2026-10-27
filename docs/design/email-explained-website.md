# Email, explained on the website: design

Status: the Decided points are built; the rest is discussion. Each point is
marked **Decided**, **Leaning** or **Open**; only Decided points get built.
Open questions waiting on the owner are issues labelled `question-for-user`.

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
- **On the website, a diagram is framed, in Archify's embed mode** (2026-10-25,
  owner, #822; a start, to be changed if it doesn't feel right once live). Each
  diagram is Archify's own page with `?embed=1`: the diagram alone, without the
  title, toolbar, cards and node index that repeat the chapter. It sits in a
  same-origin frame the page sizes to fit, served with a policy of its own
  (`frame-ancestors 'self'`). The site and the diagram can't affect each other,
  and a new Archify release drops in unchanged. The prototype compared eight
  ways on the real site (the table is in #822): drawing the diagram into the
  page worked only behind a rewriting layer that had to follow every Archify
  release, and still clashed when a diagram appeared twice. A link can't yet
  open a diagram focused on one node; the page can pass that into the frame
  later.
- **The diagrams keep one look, in the live design's colours and type**
  (2026-10-25, owner, #823). Archify's colours and typefaces are set to match
  the design rest-mail.com runs (Safe), and the SVG GitHub shows is exported
  the same way, so the site and GitHub never show different pictures. The
  diagrams don't change with `?design=`.
- **The widest diagrams are redrawn narrower, and phones scroll diagrams
  sideways** (2026-10-25, owner, #824; provisional, may change once seen).
  Every diagram is as wide as the text (793px in a 1440px window). A wider
  canvas shrinks to fit, which took the widest's node titles down to 6.4px on a
  laptop and 3.7px on a phone. The four widest were redrawn to keep titles at
  9.5px or more on a laptop, a canvas of at most 918px: `spf` in four columns
  (918px), `rest-mail` in four (908px), `queue` with a shorter main row (702px),
  and `open-relay` split in two, the relay decision and what it costs (898px
  each). The others are 920–1003px wide, with titles at 8.7–9.5px. On a phone
  a diagram keeps titles at 9.5px in a box that scrolls sideways, with a hint
  that it does (the website's `Diagram.tsx`).
- **This repository builds the bundle; the website takes it by itself**
  (2026-10-24, owner, who asked that a merged chapter go live with nobody
  running the steps by hand). On every change to the guide on `main`,
  `email-explained.yml` builds the bundle and pushes it as the OCI image
  `ghcr.io/rest-mail/email-explained:<version>`: one layer holding
  `bundle.json` and `diagrams/`, public, so it can be fetched with no
  credentials. The version is `<format>.0.<commits on main>`, so its major is
  the bundle's format and it rises with every publish. The website checks for a
  newer version on a schedule, commits it, runs its own tests, and releases
  itself: a tag, its image, and its chart. A failing test stops it there. The
  last step, antimatter-vps deploying the new chart, stays with the owner's
  agent on purpose (2026-10-25, owner, #826): no pipeline holds a credential
  that can deploy, so breaking into a repository or a workflow can publish a
  release but can't put it live. The agent checks for a newer chart on every
  run and deploys it behind the Pulumi preview. An OCI image, not a branch here, because every clone of this
  repository would otherwise carry every bundle's rendered diagrams (about
  20 MB each), and not a GitHub release, which would compete with the server's
  own releases.
- **The website publishes the API reference too** (2026-10-23, owner). Once
  the server serves its generated `openapi.json` (#623), the same pipeline
  turns it into reference pages on rest-mail.com, so the reference is as
  current as the code.

## Publication check, 2026-10-27

Read-only HTTP checks of [the live guide](https://rest-mail.com/email-explained)
found bundle source commit
[`48b81639491375056b1ed7efcfad1e479ce66d2a`](https://github.com/rest-mail/rest-mail-server/commit/48b81639491375056b1ed7efcfad1e479ce66d2a).
All 29 chapter pages were fetched. Their 27 distinct diagram documents use
same-origin frames with `embed=1` and that source revision, and serve
`frame-ancestors 'self'`. Every diagram contains Safe's paper, ink and cobalt
colours and JetBrains Mono. The diagram sources and rendering scripts have no
changes between that revision and server `55f24354`; later guide text does.

The four redraws are present in the published documents, including both halves
of open-relay. Their SVG canvas widths and the minimum frame widths calculated
by the shipped phone code are:

| Diagram | Canvas width | Configured phone minimum |
|---|---:|---:|
| `spf` | 918px | 793px |
| `rest-mail` | 908px | 785px |
| `queue` | 702px | 607px |
| `open-relay` | 898px | 776px |
| `open-relay-cost` | 898px | 776px |

The shipped `Chapter-B54ArTI9.js` calculates those minima from the SVG width
using `ceil(width × 9.5 / 11)`. `base-FFsFlxY-.css` applies them at viewport
widths up to 600px inside an `overflow-x: auto` container. The script observes
overflow and displays “Scroll sideways to see the whole diagram →”. It also
observes the framed document's body to fit its height.

These checks establish published content and shipped layout code, not a fresh
browser measurement or touch test. The earlier 390px sideways/downward swipe
check is recorded in [#824](https://github.com/rest-mail/rest-mail-server/issues/824)
with [website #31](https://github.com/rest-mail/website/pull/31). The embedding
and layout decisions above remain dated and provisional. Fresh visual/touch
acceptance and the owner's assessment remain open under
[#822](https://github.com/rest-mail/rest-mail-server/issues/822).

## Leaning

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

1. **What the website adds of its own:** navigation between chapters, a
   contents page, search, "last updated from commit …" on every page. These
   live in the website repository and are not part of the bundle.
2. **Other repositories' documentation.** The guide may later split, with
   protocol chapters moving to their libraries' repositories. The bundle format
   should not assume one repository, so the website can merge several bundles
   later.

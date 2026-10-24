# Writing a chapter of Email, explained

This guide is public, and people will learn from it, so it has to be right.
These are the rules every chapter follows.

## The pattern

Every chapter has the same five parts, in this order:

1. **The problem.** What goes wrong without this, in plain words. No jargon
   before it is explained.
2. **How it works.** The protocol or setting, with a diagram where one helps.
3. **What goes wrong.** The common mistakes, why they are easy to make, and
   how to fix them.
4. **How REST Mail handles it.** Last, never instead of the rest. The chapter
   must be useful to someone who runs a different server.
5. **Sources.** Everything the chapter relies on.

## Every claim has a source

- **About a standard:** link the RFC, at the section if it has one.
- **About other software:** link its own documentation, and quote its words
  where they make the point. Be fair: describe what it does well, and blame
  configuration, not software, where that is the truth.
- **About REST Mail:** link the code that does it, by path. Something planned
  but not built links its issue, and says it is planned.
- **Numbers and dates** (versions, defaults, limits) come from a source, never
  from memory. If a default changed between versions, say which versions.

A claim with no source is cut, or turned into a question for an issue.

## Every problem is verified from outside

Saying a problem exists is not enough, and neither is REST Mail's own word that
it solves one. Each problem the guide describes, and each problem REST Mail
claims to solve, is backed by someone else's evidence that it is real and how
big it is:

- **Measurements:** studies, internet-wide scans, adoption statistics.
- **Reports from those who see the abuse:** blocklist operators such as
  Spamhaus, abuse desks, industry groups such as M3AAWG.
- **The requirements of the large providers:** for example Gmail's and Yahoo's
  sender rules.
- **Documented incidents.**

Prefer recent sources, and give each one's date: an old figure is labelled as
old. When no good evidence exists, say so plainly ("we have not found a
current, reliable count of …") rather than estimating.

## Files

- One folder per part, one Markdown file per chapter: `how-email-works/`,
  `security/`, `how-it-goes-wrong/`, `landscape/` and `reference/`.
- Each chapter starts with front matter: `title`, `part`, `summary`, and
  `diagram` when it has one. The website build reads it. A value with a colon in
  it goes in double quotes, so GitHub can read it as YAML.
- A page whose claims are all sourced in the chapters it links (the glossary,
  the checklist, the summary table) says `sources: linked` in its front matter
  instead of having a Sources section of its own.
- The order of the chapters is the order [README.md](README.md) lists them in.
- Links between chapters are relative, so they work on GitHub as well as on the
  website.
- Only the chapters that exist are linked from [README.md](README.md). A
  planned chapter is a line in the contents, not an empty file.

## Checking it

CI checks the guide on every pull request, as part of the required `CI` check,
so run the same checks before pushing. From `scripts/email-explained/`:

```sh
npm ci
ARCHIFY=$(node fetch-archify.mjs .archify)   # the Archify commit package.json pins
npm test                                     # the checks' and the bundler's own tests
node check.mjs --archify "$ARCHIFY"          # the guide itself
node bundle.mjs --archify "$ARCHIFY"         # and it builds into the website's bundle
```

`check.mjs` fails on:

- a link to a chapter, heading, file or line that does not exist, or one that
  leaves the repository;
- front matter that is missing `title`, `part` or `summary`, has a key the
  website does not read, or has a value GitHub cannot read as YAML;
- a chapter with no `## Sources` section (or `sources: linked`);
- a diagram no chapter shows, one without its static image, or a source Archify
  does not accept.

Outside links change on other sites' schedules, so `node check.mjs --external`
runs daily instead (`scheduled.yml`). A link that no longer answers, or an
anchor no longer on its page, opens an issue, which closes itself once they all
answer again.

## Diagrams

Diagrams are made with [Archify](https://github.com/tt-a1i/archify). Each one is
a JSON source in [`diagrams/`](diagrams/), named `<name>.<type>.json` (for
example `open-relay.workflow.json`).

- **Render and check it** from the repository root:

  ```sh
  node <archify>/bin/archify.mjs finalize workflow \
    docs/email-explained/diagrams/open-relay.workflow.json \
    docs/email-explained/diagrams/open-relay.html --quality showcase --json
  ```

  `finalize` validates the source, renders it, and checks the page in a real
  browser (set `ARCHIFY_CHROME` to a Chrome or Chromium binary). The HTML and
  its receipts are generated, and git-ignored.
- **The static image** for GitHub readers is committed beside the source
  (`<name>.png`). It is exported from the rendered page, never edited by hand:

  ```sh
  CHROME=<chrome or chromium> node scripts/email-explained/png.mjs \
    docs/email-explained/diagrams/open-relay.html docs/email-explained/diagrams/open-relay.png
  ```

  The script opens the page in its embed mode, light theme, motion stopped, and
  captures the diagram at a width that suits it.
- **The interactive version** is what the website shows, built from the same
  source.
- **A diagram's facts are the chapter's facts.** Every label that states
  something (a setting, a default, a reply code) is sourced in the chapter's
  text.

## Tone

- **Respectful, always.** Other projects are compared, never attacked. Postfix,
  Dovecot, Exim and the projects that package them have served the internet
  for decades, and many of their authors' fixes are what this guide explains.
  Say what each does well, describe a limitation as a fact with its source, and
  let REST Mail's approach make its own case. No sneering, no smears, no
  loaded words.
- Plain words, short paragraphs, one idea at a time.
- Commands and settings are copyable, in code blocks, and tested.
- Explain a term the first time it appears.

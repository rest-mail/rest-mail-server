# Desktop font collection

These font files are bundled in the desktop application. No font is fetched at runtime.

License text is preserved, with line endings and trailing whitespace normalized.

Source: Google Fonts GitHub, pinned commit `06bfe0d62a98375cb09c942fa18e68bd32cff057`. Nineteen families use SIL Open Font License 1.1; Roboto Slab uses Apache 2.0. Individual unmodified license files are next to the WOFF2 assets in `public/fonts/`.

Upright variable fonts retain their upstream weight and optical axes. Static families include regular and bold where available. Italic styles are synthesized by the renderer. Subsets include Latin Extended, common punctuation, currency and arrows. Other scripts fall back to locally installed fonts.

Total WOFF2 payload: **1,843,804 bytes (1.76 MiB)**. The machine-readable manifest records source URLs, weight ranges, exact file sizes and SHA-256 digests.

| Family | License | Source | WOFF2 bytes |
| --- | --- | --- | ---: |
| Bricolage Grotesque | SIL OFL 1.1 | [Pinned source](https://github.com/google/fonts/tree/06bfe0d62a98375cb09c942fa18e68bd32cff057/ofl/bricolagegrotesque) | 188,300 |
| Instrument Sans | SIL OFL 1.1 | [Pinned source](https://github.com/google/fonts/tree/06bfe0d62a98375cb09c942fa18e68bd32cff057/ofl/instrumentsans) | 83,516 |
| Space Grotesk | SIL OFL 1.1 | [Pinned source](https://github.com/google/fonts/tree/06bfe0d62a98375cb09c942fa18e68bd32cff057/ofl/spacegrotesk) | 40,512 |
| Parcel Sans | SIL OFL 1.1 | [Pinned source](https://github.com/google/fonts/tree/06bfe0d62a98375cb09c942fa18e68bd32cff057/ofl/ibmplexsans) | 110,992 |
| Parcel Serif | SIL OFL 1.1 | [Pinned source](https://github.com/google/fonts/tree/06bfe0d62a98375cb09c942fa18e68bd32cff057/ofl/ibmplexserif) | 59,644 |
| Parcel Mono | SIL OFL 1.1 | [Pinned source](https://github.com/google/fonts/tree/06bfe0d62a98375cb09c942fa18e68bd32cff057/ofl/ibmplexmono) | 48,224 |
| Fraunces | SIL OFL 1.1 | [Pinned source](https://github.com/google/fonts/tree/06bfe0d62a98375cb09c942fa18e68bd32cff057/ofl/fraunces) | 189,384 |
| Geist | SIL OFL 1.1 | [Pinned source](https://github.com/google/fonts/tree/06bfe0d62a98375cb09c942fa18e68bd32cff057/ofl/geist) | 43,396 |
| Geist Mono | SIL OFL 1.1 | [Pinned source](https://github.com/google/fonts/tree/06bfe0d62a98375cb09c942fa18e68bd32cff057/ofl/geistmono) | 43,360 |
| Roboto Slab | Apache 2.0 | [Pinned source](https://github.com/google/fonts/tree/06bfe0d62a98375cb09c942fa18e68bd32cff057/apache/robotoslab) | 59,080 |
| Manrope | SIL OFL 1.1 | [Pinned source](https://github.com/google/fonts/tree/06bfe0d62a98375cb09c942fa18e68bd32cff057/ofl/manrope) | 36,164 |
| DM Serif Display | SIL OFL 1.1 | [Pinned source](https://github.com/google/fonts/tree/06bfe0d62a98375cb09c942fa18e68bd32cff057/ofl/dmserifdisplay) | 28,032 |
| Newsreader | SIL OFL 1.1 | [Pinned source](https://github.com/google/fonts/tree/06bfe0d62a98375cb09c942fa18e68bd32cff057/ofl/newsreader) | 206,196 |
| JetBrains Mono | SIL OFL 1.1 | [Pinned source](https://github.com/google/fonts/tree/06bfe0d62a98375cb09c942fa18e68bd32cff057/ofl/jetbrainsmono) | 48,624 |
| Syne | SIL OFL 1.1 | [Pinned source](https://github.com/google/fonts/tree/06bfe0d62a98375cb09c942fa18e68bd32cff057/ofl/syne) | 44,660 |
| Unbounded | SIL OFL 1.1 | [Pinned source](https://github.com/google/fonts/tree/06bfe0d62a98375cb09c942fa18e68bd32cff057/ofl/unbounded) | 154,412 |
| Literata | SIL OFL 1.1 | [Pinned source](https://github.com/google/fonts/tree/06bfe0d62a98375cb09c942fa18e68bd32cff057/ofl/literata) | 244,100 |
| Archivo | SIL OFL 1.1 | [Pinned source](https://github.com/google/fonts/tree/06bfe0d62a98375cb09c942fa18e68bd32cff057/ofl/archivo) | 160,704 |
| Atkinson Hyperlegible | SIL OFL 1.1 | [Pinned source](https://github.com/google/fonts/tree/06bfe0d62a98375cb09c942fa18e68bd32cff057/ofl/atkinsonhyperlegible) | 38,872 |
| Silkscreen | SIL OFL 1.1 | [Pinned source](https://github.com/google/fonts/tree/06bfe0d62a98375cb09c942fa18e68bd32cff057/ofl/silkscreen) | 15,632 |

## Reproduce

Run `python desktop/scripts/bundle-fonts.py` using Python with `fonttools[woff]` (generated with fontTools 4.68.0) available. This is a maintainer-only download/subsetting step, never part of the runtime or normal build. Do not edit generated font assets without regenerating the manifest.

## Modified-font naming

Parcel Sans, Parcel Serif and Parcel Mono are Latin-subset derivatives of IBM Plex Sans, IBM Plex Serif and IBM Plex Mono. Their internal family and PostScript names have been renamed to respect the upstream reserved name. Copyright and license notices are preserved. Roboto Slab's separate upstream copyright notice is also included.

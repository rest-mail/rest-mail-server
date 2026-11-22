"""Reproduce the desktop's Latin-only, offline font collection with fonttools[woff]."""
import hashlib
import json
from pathlib import Path
import tempfile
from urllib.parse import quote, unquote
from urllib.request import urlopen

from fontTools import subset
from fontTools.ttLib import TTFont

COMMIT = "06bfe0d62a98375cb09c942fa18e68bd32cff057"
FAMILIES = [
    "Bricolage Grotesque", "Instrument Sans", "Space Grotesk", "IBM Plex Sans",
    "IBM Plex Serif", "IBM Plex Mono", "Fraunces", "Geist", "Geist Mono",
    "Roboto Slab", "Manrope", "DM Serif Display", "Newsreader", "JetBrains Mono",
    "Syne", "Unbounded", "Literata", "Archivo", "Atkinson Hyperlegible", "Silkscreen",
]
UNICODES = "U+0000-024F,U+1E00-1EFF,U+2000-206F,U+20A0-20CF,U+2100-214F,U+2190-21FF,U+2212,U+FEFF,U+FFFD"
ALIASES = {"IBM Plex Sans": "Parcel Sans", "IBM Plex Serif": "Parcel Serif", "IBM Plex Mono": "Parcel Mono"}
FRONTEND = Path(__file__).resolve().parents[1] / "frontend"
DESTINATION = FRONTEND / "public" / "fonts"


def download(url):
    with urlopen(url, timeout=90) as response:
        return response.read()


def main():
    DESTINATION.mkdir(parents=True, exist_ok=True)
    manifest_path = DESTINATION / "manifest.json"
    cached = json.loads(manifest_path.read_text()) if manifest_path.exists() else []
    records = []
    rules = []
    for family in FAMILIES:
        slug = family.lower().replace(" ", "")
        display_family = ALIASES.get(family, family)
        directory = f"{'apache' if slug == 'robotoslab' else 'ofl'}/{slug}"
        license_name = "Apache 2.0" if slug == "robotoslab" else "SIL OFL 1.1"
        source = f"https://github.com/google/fonts/tree/{COMMIT}/{directory}"
        previous = next((record for record in cached if record.get("upstream_family", record["family"]) == family and record["commit"] == COMMIT), None)
        if previous:
            files = [{"name": unquote(asset["source"].rsplit("/", 1)[1]), "download_url": asset["source"]} for asset in previous["assets"]]
            license_file = "LICENSE.txt" if slug == "robotoslab" else "OFL.txt"
            files.append({"name": license_file, "download_url": f"https://raw.githubusercontent.com/google/fonts/{COMMIT}/{directory}/{license_file}"})
            if slug == "robotoslab":
                files.append({"name": "COPYRIGHT.txt", "download_url": f"https://raw.githubusercontent.com/google/fonts/{COMMIT}/{directory}/COPYRIGHT.txt"})
        else:
            files = json.loads(download(f"https://api.github.com/repos/google/fonts/contents/{directory}?ref={COMMIT}"))
        files = list({item["name"]: item for item in files}.values())
        licence = next(item for item in files if item["name"] in ("OFL.txt", "LICENSE.txt"))
        license_path = DESTINATION / f"{slug}-{licence['name']}"
        if not license_path.exists():
            license_path.write_text("\n".join(line.rstrip() for line in download(licence["download_url"]).decode().splitlines()) + "\n")
        for notice in files:
            if notice["name"] == "COPYRIGHT.txt":
                notice_path = DESTINATION / f"{slug}-COPYRIGHT.txt"
                if not notice_path.exists():
                    notice_path.write_bytes(download(notice["download_url"]))
        candidates = [item for item in files if item["name"].endswith(".ttf") and "Italic" not in item["name"]]
        variable = [item for item in candidates if "[" in item["name"]]
        selected = variable[:1] or [item for item in candidates if item["name"].endswith(("-Regular.ttf", "-Bold.ttf"))]
        if not selected:
            raise RuntimeError(f"No upright fonts found: {family}")
        assets = []
        for item in selected:
            raw_url = f"https://raw.githubusercontent.com/google/fonts/{COMMIT}/{directory}/{quote(item['name'])}"
            original = download(raw_url)
            with tempfile.NamedTemporaryFile(suffix=".ttf") as temporary:
                temporary.write(original)
                temporary.flush()
                font = TTFont(temporary.name, recalcTimestamp=False)
                weight = str(font["OS/2"].usWeightClass)
                if "fvar" in font:
                    axis = next((axis for axis in font["fvar"].axes if axis.axisTag == "wght"), None)
                    if axis:
                        weight = f"{int(axis.minValue)} {int(axis.maxValue)}"
                options = subset.Options()
                options.flavor = "woff2"
                options.layout_features = ["*"]
                options.name_IDs = [0, 1, 2, 3, 4, 5, 6, 13, 14]
                subsetter = subset.Subsetter(options=options)
                subsetter.populate(unicodes=subset.parse_unicodes(UNICODES))
                subsetter.subset(font)
                if family in ALIASES:
                    for record in font["name"].names:
                        if record.nameID in (1, 3, 4, 6, 16, 17, 25):
                            text = record.toUnicode().replace("IBM Plex", "Parcel").replace("IBMPlex", "Parcel")
                            record.string = text.encode(record.getEncoding())
                font.flavor = "woff2"
                suffix = "variable" if " " in weight else weight
                name = f"{slug}-{suffix}-latin.woff2"
                asset = DESTINATION / name
                existing = TTFont(asset) if asset.exists() and family in ALIASES else None
                if not asset.exists() or existing and "Plex" in str(existing["name"].getDebugName(1)):
                    font.save(asset)
                assets.append({"file": name, "bytes": asset.stat().st_size, "sha256": hashlib.sha256(asset.read_bytes()).hexdigest(), "source": raw_url, "weight": weight})
                rules.append(f'@font-face {{ font-family: "{display_family}"; font-style: normal; font-weight: {weight}; font-display: swap; src: url("/fonts/{name}") format("woff2"); }}')
        records.append({"family": display_family, "upstream_family": family, "license": license_name, "source": source, "commit": COMMIT, "assets": assets})
        print(f"{family}: {sum(asset['bytes'] for asset in assets):,} bytes", flush=True)
    (FRONTEND / "src" / "appearance" / "fonts.css").write_text("\n".join(rules) + "\n")
    (DESTINATION / "manifest.json").write_text(json.dumps(records, indent=2) + "\n")
    total = sum(asset["bytes"] for record in records for asset in record["assets"])
    documentation = ["# Desktop font collection", "", "These font files are bundled in the desktop application. No font is fetched at runtime.", "", f"Source: Google Fonts GitHub, pinned commit `{COMMIT}`. Nineteen families use SIL Open Font License 1.1; Roboto Slab uses Apache 2.0. Individual unmodified license files are next to the WOFF2 assets in `public/fonts/`.", "", "Upright variable fonts retain their upstream weight and optical axes. Static families include regular and bold where available. Italic styles are synthesized by the renderer. Subsets include Latin Extended, common punctuation, currency and arrows. Other scripts fall back to locally installed fonts.", "", f"Total WOFF2 payload: **{total:,} bytes ({total / 1024 / 1024:.2f} MiB)**. The machine-readable manifest records source URLs, weight ranges, exact file sizes and SHA-256 digests.", "", "| Family | License | Source | WOFF2 bytes |", "| --- | --- | --- | ---: |"]
    for record in records:
        documentation.append(f"| {record['family']} | {record['license']} | [Pinned source]({record['source']}) | {sum(asset['bytes'] for asset in record['assets']):,} |")
    documentation.extend(["", "## Reproduce", "", "Run `python desktop/scripts/bundle-fonts.py` using Python with `fonttools[woff]` (generated with fontTools 4.68.0) available. This is a maintainer-only download/subsetting step, never part of the runtime or normal build. Do not edit generated font assets without regenerating the manifest.", ""])
    documentation.insert(4, "License text is preserved, with line endings and trailing whitespace normalized.\n")
    documentation.extend(["## Modified-font naming", "", "Parcel Sans, Parcel Serif and Parcel Mono are Latin-subset derivatives of IBM Plex Sans, IBM Plex Serif and IBM Plex Mono. Their internal family and PostScript names have been renamed to respect the upstream reserved name. Copyright and license notices are preserved. Roboto Slab's separate upstream copyright notice is also included.", ""])
    (FRONTEND / "FONTS.md").write_text("\n".join(documentation))


if __name__ == "__main__":
    main()

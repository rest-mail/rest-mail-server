"""Tailwind in webmail and admin reads class names from source files only.

Without source() on the Tailwind import, Tailwind scans every text file under
the build root, which in the images is the whole app directory including the
Dockerfile. A word in a comment there then adds a CSS rule and changes every
asset hash (#1854).
"""

from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[2]

# Each app's Tailwind entry and the file that imports it into the bundle.
ENTRIES = {
    "admin": ("src/styles.css", "src/routes/__root.tsx", "'../styles.css'"),
    "webmail": ("src/index.css", "src/main.tsx", "'./index.css'"),
}

TAILWIND_IMPORT = re.compile(r'@import\s+"tailwindcss"([^;]*);')


class TailwindSourcesTests(unittest.TestCase):
    def test_entries_scan_only_source(self):
        for app, (entry, importer, specifier) in ENTRIES.items():
            with self.subTest(app=app):
                base = ROOT / app
                # The entry checked here must be the one the app builds.
                self.assertIn("import " + specifier,
                              (base / importer).read_text())
                css = (base / entry).read_text()
                imports = TAILWIND_IMPORT.findall(css)
                self.assertEqual(len(imports), 1, imports)
                self.assertEqual(imports[0].strip(), 'source("../src")')
                self.assertIn('@source "../index.html";', css)
                # Nothing may widen the scan back to the app root.
                for path in re.findall(r'@source\s+"([^"]+)"', css):
                    resolved = (base / entry).parent.joinpath(path).resolve()
                    self.assertTrue(
                        resolved == base / "index.html"
                        or resolved.is_relative_to(base / "src"),
                        f"{app}: @source {path!r} reaches outside src/")

    def test_no_other_tailwind_entry(self):
        for app in ENTRIES:
            with self.subTest(app=app):
                entries = [
                    path.relative_to(ROOT / app).as_posix()
                    for path in (ROOT / app / "src").rglob("*.css")
                    if TAILWIND_IMPORT.search(path.read_text())
                ]
                self.assertEqual(entries, [ENTRIES[app][0]])


if __name__ == "__main__":
    unittest.main()

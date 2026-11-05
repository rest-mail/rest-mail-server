"""The testbed's DNS answers for .test itself, whatever public resolvers do."""

from pathlib import Path
import unittest


FRAGMENT = Path(__file__).resolve().parents[2] / "projects/dnsmasq/fragment.tmpl"


class TestbedDNSFragmentTests(unittest.TestCase):
    def test_test_names_are_never_forwarded(self):
        # dnsmasq answers an A query from its config but forwards the AAAA query
        # for the same name. Go asks for both and waits for both, so with no answer
        # from upstream a stage 1 dial failed: "lookup mail1.test: i/o timeout".
        lines = [line.strip() for line in FRAGMENT.read_text().splitlines()]
        self.assertIn("local=/test/", lines)


if __name__ == "__main__":
    unittest.main()

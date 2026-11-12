"""tests/e2e needs the full topology, so read what it sends as RESTMAIL's raw_message (#1921)."""

import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
E2E = ROOT / "tests/e2e"
# A key given a value. A struct tag or a read such as body["raw_message"].(string) gives none.
FIELD = re.compile(r'\\?"raw_message\\?"(?:\s*:|\]\s*=(?!=))\s*([^\n]*)')
# json.Marshal writes []byte as base64, so both of these put base64 on the wire.
BASE64 = ("base64.", "[]byte(")


def text_values(source):
    """Every raw_message value in a Go source that is not plainly base64."""
    return [value for value in FIELD.findall(source) if not value.startswith(BASE64)]


class E2eRawMessageTest(unittest.TestCase):
    def test_e2e_sends_raw_message_only_as_base64(self):
        sources = {path: path.read_text(encoding="utf-8") for path in sorted(E2E.glob("*.go"))}
        self.assertTrue(any(FIELD.search(source) for source in sources.values()), "no raw_message found in tests/e2e")
        for path, source in sources.items():
            self.assertEqual(text_values(source), [], path.relative_to(ROOT))

    def test_text_is_caught_in_each_form_a_request_can_take(self):
        for line in (
            '"raw_message": raw,',
            '"raw_message":\n\t\traw,',
            'body["raw_message"] = raw',
            '`{"raw_message":"From: a@b.test"}`',
            '"{\\"raw_message\\":\\"From: a@b.test\\"}"',
        ):
            self.assertEqual(len(text_values(line)), 1, line)

    def test_base64_and_reads_pass(self):
        for line in (
            '"raw_message": base64.StdEncoding.EncodeToString([]byte(raw)),',
            '"raw_message":\n\t\tbase64.StdEncoding.EncodeToString([]byte(raw)),',
            '"raw_message": []byte(raw),',
            'body["raw_message"] = []byte(raw)',
            'RawMessage string `json:"raw_message"`',
            'source := sent["raw_message"].(string)',
            'if sent["raw_message"] == want {',
        ):
            self.assertEqual(text_values(line), [], line)


if __name__ == "__main__":
    unittest.main()

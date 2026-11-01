"""Keep topology download examples paired, with no response excluded."""

import re
import unittest
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]
PATH = "/api/v1/attachments/{id}"


class AttachmentContract(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        with (ROOT / "openapi.yaml").open() as source:
            cls.path = yaml.safe_load(source)["paths"][PATH]
        with (ROOT / "vertrag.yml").open() as source:
            config = yaml.safe_load(source)
        cls.patterns = [re.compile(p) for p in config.get("exclude-matching") or []]
        cls.exclusions = config["surface"]["exclude"]

    def test_named_request_headers_pair_only_with_their_response(self):
        op = self.path["get"]
        params = {p["name"]: p for p in op["parameters"]}
        self.assertEqual(
            params["Range"]["examples"],
            {
                "range_single": {"value": "bytes=0-0"},
                "range_not_satisfiable": {"value": "bytes=9223372036854775806-"},
            },
        )
        self.assertEqual(
            self.path["parameters"][0]["examples"], {"found": {"value": 1}}
        )
        for status, media, name in (
            ("200", "application/octet-stream", "found"),
            ("206", "application/octet-stream", "range_single"),
            ("416", "text/plain", "range_not_satisfiable"),
        ):
            with self.subTest(status=status):
                content = op["responses"][status]["content"][media]
                self.assertNotIn("example", content)
                self.assertEqual(set(content["examples"]), {name})
        for param in params.values():
            self.assertFalse(param["required"])
            self.assertNotIn("example", param)

    def test_no_exclusion_hides_a_download_response(self):
        for status, media in (
            ("200", "application/octet-stream"),
            ("206", "application/octet-stream"),
            ("206", "multipart/byteranges"),
            ("304", ""),
            ("412", ""),
            ("416", "text/plain"),
            ("500", "application/json"),
        ):
            name = f"{PATH} > Download an attachment > {status}"
            if media:
                name += " > " + media
            with self.subTest(name=name):
                self.assertFalse(any(p.search(name) for p in self.patterns))

    def test_optional_response_headers_remain_optional(self):
        for status in ("200", "206", "304", "412", "416", "500"):
            for name in ("ETag", "Last-Modified"):
                with self.subTest(status=status, header=name):
                    self.assertFalse(
                        self.path["get"]["responses"][status]["headers"][name].get(
                            "required", False
                        )
                    )
        self.assertFalse(
            self.path["get"]["responses"]["206"]["headers"]["Content-Range"].get(
                "required", False
            )
        )

    def test_every_download_status_counts_toward_the_surface(self):
        self.assertEqual(
            [e for e in self.exclusions if e.get("operation") == "getAttachment"], []
        )

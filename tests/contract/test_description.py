"""The live contract description is required, saved once, and never stale."""
import io
import json
from pathlib import Path
import tempfile
import unittest
import urllib.error
from unittest.mock import patch

import description


class DescriptionTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.output = Path(directory.name) / "openapi.json"
        self.config = {"endpoint": "http://fixture.test:8080"}

    def test_fetches_the_selected_server_and_preserves_its_document(self):
        body = b'{"openapi":"3.1.0","paths":{"/new":{"get":{"operationId":"new"}}}}'
        for endpoint, expected in (("", "http://fixture.test:8080"),
                                   ("http://other.test:8081/", "http://other.test:8081")):
            with self.subTest(endpoint=endpoint):
                with patch.object(description.urllib.request, "urlopen", return_value=io.BytesIO(body)) as send:
                    description.fetch(self.config, self.output, endpoint)
                send.assert_called_once_with(expected + "/api/docs/openapi.json", timeout=30)
                self.assertEqual(self.output.read_bytes(), body)

    def test_fetch_failure_removes_a_previous_snapshot(self):
        self.output.write_text("previous run")
        with patch.object(description.urllib.request, "urlopen", side_effect=urllib.error.URLError("unreachable")):
            with self.assertRaises(urllib.error.URLError):
                description.fetch(self.config, self.output)
        self.assertFalse(self.output.exists())

    def test_invalid_responses_never_become_a_description(self):
        for document in ([], {}, {"openapi": "2.0", "paths": {}}, {"openapi": "3.1.0", "paths": []}):
            with self.subTest(document=document):
                self.output.write_text("previous run")
                with patch.object(description.urllib.request, "urlopen", return_value=io.BytesIO(json.dumps(document).encode())):
                    with self.assertRaises(ValueError):
                        description.fetch(self.config, self.output)
                self.assertFalse(self.output.exists())


if __name__ == "__main__":
    unittest.main()

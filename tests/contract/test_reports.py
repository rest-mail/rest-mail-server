import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import reports  # noqa: E402

ENDPOINTS = [{"endpoint": "https://10.99.0.20:8443", "operation-id": ["deliverMessage"]}]


class Endpoints(unittest.TestCase):
    def configured(self, endpoint):
        config = {"endpoint": "http://10.99.0.20:8080", "endpoints": list(ENDPOINTS)}
        return reports.configure(config, ".", "out", endpoint=endpoint)

    def test_this_topology_keeps_its_internal_listener(self):
        self.assertEqual(self.configured("").get("endpoints"), ENDPOINTS)
        self.assertEqual(self.configured("http://10.99.0.20:8080").get("endpoints"), ENDPOINTS)

    def test_another_server_is_not_sent_to_this_topologys_listener(self):
        config = self.configured("http://other:8080")
        self.assertEqual(config["endpoint"], "http://other:8080")
        self.assertNotIn("endpoints", config)


if __name__ == "__main__":
    unittest.main()

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

    def test_a_private_topology_names_where_its_internal_listener_is(self):
        # #2062: the host reaches a private run's API through forwarded loopback ports.
        config = {"endpoint": "http://10.99.0.20:8080", "endpoints": [dict(entry) for entry in ENDPOINTS]}
        configured = reports.configure(config, ".", "out", endpoint="http://127.0.0.1:50000", internal_endpoint="https://127.0.0.1:50001")
        self.assertEqual(configured["endpoint"], "http://127.0.0.1:50000")
        self.assertEqual([entry["endpoint"] for entry in configured["endpoints"]], ["https://127.0.0.1:50001"])
        self.assertEqual(configured["endpoints"][0]["operation-id"], ["deliverMessage"])


if __name__ == "__main__":
    unittest.main()

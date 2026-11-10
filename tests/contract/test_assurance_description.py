import unittest
from pathlib import Path

import yaml


class AssuranceDescription(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.document = yaml.safe_load(
            (Path(__file__).resolve().parents[2] / "openapi.yaml").read_text()
        )

    def test_grant_and_link_operations_distinguish_session_assurance(self):
        for path, method in (
            ("/api/v1/accounts/link", "post"),
            ("/api/v1/accounts/{id}", "delete"),
            ("/api/v1/admin/grants/{id}/activate", "post"),
            ("/api/v1/admin/grants/{id}/deactivate", "post"),
            ("/api/v1/admin/grants/{id}/make-eligible", "post"),
        ):
            with self.subTest(path=path):
                description = self.document["paths"][path][method]["description"]
                self.assertIn("session signed in with two-factor authentication", description)

    def test_deactivation_does_not_claim_a_password_session_is_enough(self):
        description = self.document["paths"]["/api/v1/admin/grants/{id}/deactivate"]["post"]["description"]
        self.assertNotIn("Needs no second factor", description)
        self.assertIn("No extra code in the request body", description)


if __name__ == "__main__":
    unittest.main()

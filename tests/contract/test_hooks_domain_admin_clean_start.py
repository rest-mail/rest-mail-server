"""The domain administration lists and the certificate operations start clean
in the own-data run (#1385), and a listing that succeeds after a reset must
hold what its before hook made (#1789)."""

import json
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

from test_hooks_pipeline import load_hooks


def transaction(path, name, body=None):
    return {
        "protocol": "http:", "host": "api", "port": "8080",
        "fullPath": path, "name": name,
        "request": {"body": json.dumps(body), "headers": {}},
    }


class CleanStart(unittest.TestCase):
    def setUp(self):
        self.hooks = load_hooks()
        self.env = patch.dict("os.environ", {"CONTRACT_RESET_SEED": "1"})
        self.env.start()
        self.addCleanup(self.env.stop)
        self.globals = self.hooks.domain_administration_after_a_reset.__globals__
        self.events = []

        def fixture_call(transaction, method, path, body=None):
            self.events.append((method, path))
            return {"id": 41}

        self.patch = patch.dict(self.globals)
        names = self.patch.start()
        self.addCleanup(self.patch.stop)
        names["fixture_call"] = fixture_call
        names["openssl"] = Mock(side_effect=self.write_pem)
        names["start_clean"] = Mock(side_effect=lambda t: self.events.append("reset"))
        self.hooks.hooks.stash.clear()
        self.addCleanup(self.hooks.hooks.stash.clear)

    @staticmethod
    def write_pem(*args):
        for flag in ("-keyout", "-out"):
            with open(args[args.index(flag) + 1], "w") as pem:
                pem.write("pem")

    def made(self):
        return self.hooks.hooks.stash.get("listing_made")

    def test_each_listing_gets_a_row_of_its_own_after_its_reset(self):
        domain = ("POST", "/api/v1/admin/domains")
        for path, calls, what, match in (
            ("/api/v1/admin/domains", [domain], "domain", {"id": 41}),
            (
                "/api/v1/admin/aliases",
                [domain, ("POST", "/api/v1/admin/aliases")],
                "alias",
                {"id": 41},
            ),
            (
                "/api/v1/admin/dkim",
                [domain, ("PUT", "/api/v1/admin/dkim/41")],
                "DKIM key",
                {"domain_id": 41, "selector": "contract", "has_key": True},
            ),
            (
                "/api/v1/admin/certificates",
                [domain, ("POST", "/api/v1/admin/certificates")],
                "certificate",
                {"id": 41},
            ),
        ):
            with self.subTest(path=path):
                self.events.clear()
                sent = transaction(path, path + " > 200 > application/json")
                self.hooks.domain_administration_after_a_reset(sent)
                self.assertEqual(self.events, ["reset"] + calls)
                self.assertEqual(sent["fullPath"], path)
                self.assertEqual(
                    self.made(),
                    {"name": sent["name"], "what": what, "match": match},
                )

    def test_a_certificate_read_or_delete_gets_a_certificate_of_its_own(self):
        for name in ("x > 200 > application/json", "x > 204", "x > 401 > application/json"):
            with self.subTest(name=name):
                self.events.clear()
                sent = transaction("/api/v1/admin/certificates/1?x=1", name)
                self.hooks.domain_administration_after_a_reset(sent)
                self.assertEqual(
                    self.events,
                    ["reset", ("POST", "/api/v1/admin/domains"), ("POST", "/api/v1/admin/certificates")],
                )
                self.assertEqual(sent["fullPath"], "/api/v1/admin/certificates/41?x=1")

    def test_the_rest_only_reset(self):
        for path, name in (
            ("/api/v1/admin/certificates/2147483647", "x > 404 > application/json"),
            ("/api/v1/admin/certificates/0", "x > 404 > application/json"),
            ("/api/v1/admin/certificates", "x > 201 > application/json"),
            ("/api/v1/admin/certificates", "x > 422 > application/json"),
            ("/api/v1/admin/aliases?domain_id=x", "x > 422 > application/json"),
            ("/api/v1/admin/domains", "x > 401 > application/json"),
        ):
            with self.subTest(path=path, name=name):
                self.events.clear()
                self.hooks.hooks.stash.clear()
                sent = transaction(path, name)
                self.hooks.domain_administration_after_a_reset(sent)
                self.assertEqual(self.events, ["reset"])
                self.assertEqual(sent["fullPath"], path)
                self.assertIsNone(self.made())

    def test_a_custom_filter_listing_notes_its_filter(self):
        sent = transaction("/api/v1/admin/custom-filters", "x > 200 > application/json")
        self.hooks.filter_requests_after_a_reset(sent)
        self.assertEqual(
            self.made(), {"name": sent["name"], "what": "custom filter", "match": {"id": 41}}
        )

    def test_without_a_reset_nothing_changes(self):
        with patch.dict("os.environ", {"CONTRACT_RESET_SEED": ""}):
            sent = transaction("/api/v1/admin/certificates/1", "x > 200 > application/json")
            self.hooks.domain_administration_after_a_reset(sent)
        self.assertEqual(self.events, [])
        self.assertEqual(sent["fullPath"], "/api/v1/admin/certificates/1")


class ListingHoldsWhatWasMade(unittest.TestCase):
    def setUp(self):
        self.hooks = load_hooks()
        self.env = patch.dict("os.environ", {"CONTRACT_RESET_SEED": "1"})
        self.env.start()
        self.addCleanup(self.env.stop)
        self.hooks.hooks.stash.clear()
        self.addCleanup(self.hooks.hooks.stash.clear)

    def answered(self, rows, status=200, name="x > 200 > application/json"):
        sent = transaction("/api/v1/admin/custom-filters", name)
        sent["real"] = {"statusCode": status, "body": json.dumps({"data": rows})}
        return sent

    def note(self, sent, match, what="custom filter"):
        self.hooks.made_for_the_listing(sent, what, match)

    def test_the_after_hook_is_registered_for_each_covered_listing(self):
        # The stub's after() registers nothing, so read the source.
        source = (Path(__file__).resolve().parent / "hooks.py").read_text()
        for operation in (
            "listCustomFilters", "listDomains", "listAliases", "listDKIMKeys", "listCertificates",
        ):
            self.assertIn(f'@hooks.after("{operation}")\n', source, operation)

    def test_it_passes_when_the_row_is_listed(self):
        sent = self.answered([{"id": 3}, {"id": 41}])
        self.note(sent, {"id": 41})
        self.hooks.the_listing_holds_what_was_made(sent)
        self.assertNotIn("fail", sent)
        self.assertNotIn("listing_made", self.hooks.hooks.stash)

    def test_it_matches_every_field_and_nested_ones(self):
        sent = self.answered([{"domain_id": 41, "selector": "contract", "has_key": True, "d": {"n": 1}}])
        self.note(sent, {"domain_id": 41, "has_key": True, "d.n": 1}, "DKIM key")
        self.hooks.the_listing_holds_what_was_made(sent)
        self.assertNotIn("fail", sent)

    def test_an_empty_list_fails(self):
        sent = self.answered([])
        self.note(sent, {"id": 41})
        self.hooks.the_listing_holds_what_was_made(sent)
        self.assertEqual(
            sent["fail"],
            "the listing does not hold the custom filter made for it (id 41): it answered 0 other rows",
        )

    def test_a_wrong_row_fails(self):
        sent = self.answered([{"id": 3}])
        self.note(sent, {"id": 41})
        self.hooks.the_listing_holds_what_was_made(sent)
        self.assertIn("it answered 1 other row", sent["fail"])

    def test_nothing_made_fails(self):
        sent = self.answered([{"id": 41}])
        self.hooks.the_listing_holds_what_was_made(sent)
        self.assertEqual(sent["fail"], "the listing had nothing made for it to list (#1785, #1789)")

    def test_another_exchanges_note_is_dropped(self):
        other = self.answered([], name="y > 200 > application/json")
        self.note(other, {"id": 41})
        sent = self.answered([{"id": 41}])
        self.hooks.the_listing_holds_what_was_made(sent)
        self.assertIn("nothing made", sent["fail"])

    def test_a_page_not_meant_to_list_it_passes(self):
        sent = self.answered([])
        self.note(sent, None)
        self.hooks.the_listing_holds_what_was_made(sent)
        self.assertNotIn("fail", sent)

    def test_refusals_and_other_statuses_are_left_alone(self):
        for status, name in (
            (422, "x > 422 > application/json"),
            (500, "x > 200 > application/json"),
        ):
            with self.subTest(status=status, name=name):
                sent = self.answered([], status, name)
                self.hooks.the_listing_holds_what_was_made(sent)
                self.assertNotIn("fail", sent)

    def test_a_run_without_a_reset_is_left_alone(self):
        with patch.dict("os.environ", {"CONTRACT_RESET_SEED": ""}):
            sent = self.answered([])
            self.hooks.the_listing_holds_what_was_made(sent)
        self.assertNotIn("fail", sent)


if __name__ == "__main__":
    unittest.main()

"""clearSuppression and getDmarcReport start clean in the own-data run
(#1385): each exchange resets first. The clear's 204 clears a suppression made
after the reset by a message to a domain that does not exist, which the
gateway's queue worker bounces; the read's 200 reads a report delivered after
the reset through deliverMessage."""

import base64
import email
import json
import os
import unittest
from unittest.mock import Mock, patch

from test_hooks_pipeline import load_hooks

PATH = "/api/v1/admin/domains/1/suppressions/1"
NAME = "/api/v1/admin/domains/{id}/suppressions/{sid} > Clear a suppression > "


def transaction(status):
    # A 204 names no media type; a refusal names application/json.
    return {
        "protocol": "http:", "host": "api", "port": "8080",
        "fullPath": PATH,
        "name": NAME + (status if status == "204" else status + " > application/json"),
        "request": {"headers": {"Authorization": "Bearer session"}},
    }


class Clock:
    """time as the hook sees it: a clock that moves only when the hook sleeps."""

    def __init__(self):
        self.now = 0.0
        self.slept = 0

    def time_ns(self):
        return 1234

    def monotonic(self):
        return self.now

    def sleep(self, seconds):
        self.slept += 1
        self.now += seconds


class CleanStart(unittest.TestCase):
    def setUp(self):
        self.hooks = load_hooks()
        self.hook = self.hooks.clear_a_suppression_made_after_the_reset
        self.globals = self.hook.__globals__
        self.patch = patch.dict(self.globals)
        names = self.patch.start()
        self.addCleanup(self.patch.stop)
        self.start_clean = Mock()
        names["start_clean"] = self.start_clean
        self.clock = Clock()
        names["time"] = self.clock
        self.made = []
        # The suppression appears on the third listing, as the queue worker
        # takes a poll or two to bounce the message.
        self.listings_before_bounce = 2
        self.domain = self.globals["FIXTURE_PLAN"]["domain"]

        def fixture_call(transaction, method, path, body=None, expect=(200, 201, 204)):
            self.made.append((method, path, body))
            if path == "/api/v1/admin/domains":
                return [{"id": 3, "name": "other.test"}, {"id": 9, "name": self.domain}]
            if path == "/api/v1/admin/domains/9/suppressions":
                if self.listings_before_bounce is None or self.listings_before_bounce > 0:
                    if self.listings_before_bounce:
                        self.listings_before_bounce -= 1
                    return [{"id": 4, "address": "someone-else@nowhere.invalid"}]
                sent_to = next(b["to"][0] for m, p, b in self.made if p == "/api/v1/messages/send")
                return [{"id": 4, "address": "someone-else@nowhere.invalid"}, {"id": 21, "address": sent_to}]
            return {"id": 7}

        names["fixture_call"] = fixture_call
        env = patch.dict("os.environ")
        env.start()
        self.addCleanup(env.stop)
        os.environ["CONTRACT_RESET_SEED"] = "1"

    def test_it_is_the_first_clear_suppression_hook(self):
        names = [h.__name__ for h in self.hooks.registered_before["clearSuppression"]]
        self.assertEqual(names[0], self.hook.__name__)
        self.assertIn("clear_the_fixture_suppression", names)

    def test_a_refusal_resets_first_and_keeps_its_path(self):
        sent = transaction("404")
        before = json.dumps(sent, sort_keys=True)
        self.hook(sent)
        self.start_clean.assert_called_once_with(sent)
        self.assertEqual(json.dumps(sent, sort_keys=True), before)
        self.assertEqual(self.made, [])

    def test_nothing_happens_without_a_reset(self):
        os.environ.pop("CONTRACT_RESET_SEED")
        sent = transaction("204")
        self.hook(sent)
        self.assertEqual(sent["fullPath"], PATH)
        self.start_clean.assert_not_called()
        self.assertEqual(self.made, [])

    def test_the_success_clears_the_suppression_of_a_bounced_message(self):
        sent = transaction("204")
        self.hook(sent)
        self.start_clean.assert_called_once_with(sent)
        sends = [(m, b) for m, p, b in self.made if p == "/api/v1/messages/send"]
        self.assertEqual(len(sends), 1)
        method, body = sends[0]
        self.assertEqual(method, "POST")
        self.assertEqual(body["from"], self.globals["ADMIN"])
        self.assertEqual(body["to"], ["contract-suppressed-1234@nowhere.invalid"])
        # The send comes after the reset's domain lookup, and the listing is
        # read until the bounce suppressed the address.
        self.assertEqual(
            [p for m, p, b in self.made if m == "GET"],
            ["/api/v1/admin/domains"] + ["/api/v1/admin/domains/9/suppressions"] * 3,
        )
        self.assertEqual(self.clock.slept, 2)
        self.assertEqual(sent["fullPath"], "/api/v1/admin/domains/9/suppressions/21")

    def test_a_message_that_never_bounces_is_an_error_not_a_pass(self):
        self.listings_before_bounce = None
        sent = transaction("204")
        with self.assertRaisesRegex(RuntimeError, "was not suppressed within 90s"):
            self.hook(sent)
        self.assertEqual(sent["fullPath"], PATH)
        self.assertGreaterEqual(self.clock.now, 90)

    def test_a_seed_without_the_fixture_domain_is_an_error(self):
        self.domain = "elsewhere.test"
        sent = transaction("204")
        with self.assertRaisesRegex(RuntimeError, "the seed hosts no"):
            self.hook(sent)
        self.assertFalse([p for m, p, b in self.made if p == "/api/v1/messages/send"])

    def test_the_fixture_hook_after_it_leaves_its_path(self):
        # clear_the_fixture_suppression acts only on the example's own path,
        # which this hook has replaced.
        sent = transaction("204")
        self.hook(sent)
        self.assertNotEqual(sent["fullPath"], PATH)



REPORT_PATH = "/api/v1/admin/dmarc-reports/1"
REPORT_NAME = "/api/v1/admin/dmarc-reports/{id} > Read a received DMARC aggregate report > "


def report_transaction(status):
    return {
        "protocol": "http:", "host": "api", "port": "8080",
        "fullPath": REPORT_PATH, "name": REPORT_NAME + status + " > application/json",
        "request": {"headers": {"Authorization": "Bearer session"}},
    }


class Delivered:
    def __init__(self, status=201):
        self.status = status

    def __enter__(self):
        return self

    def __exit__(self, *_):
        return False


class DmarcReportCleanStart(unittest.TestCase):
    def setUp(self):
        self.hooks = load_hooks()
        self.hook = self.hooks.read_a_dmarc_report_delivered_after_the_reset
        self.globals = self.hook.__globals__
        self.patch = patch.dict(self.globals)
        names = self.patch.start()
        self.addCleanup(self.patch.stop)
        self.start_clean = Mock()
        names["start_clean"] = self.start_clean
        self.made = []
        self.stored = True
        self.domain = self.globals["FIXTURE_PLAN"]["domain"]

        def fixture_call(transaction, method, path, body=None, expect=(200, 201, 204)):
            self.made.append((method, path, body))
            if path == "/api/v1/admin/domains":
                return [{"id": 9, "name": self.domain}]
            if path.startswith("/api/v1/admin/dmarc-reports"):
                reports = [{"id": 2, "report_id": "someone-elses"}]
                if self.stored:
                    reports.append({"id": 5, "report_id": self.globals["OWN_DMARC_REPORT_ID"]})
                return reports
            return {"id": 7}

        names["fixture_call"] = fixture_call
        self.delivered = []
        self.urlopen = patch.object(
            self.globals["urllib"].request, "urlopen",
            side_effect=lambda request, timeout: self.delivered.append(request) or Delivered())
        self.urlopen.start()
        self.addCleanup(self.urlopen.stop)
        env = patch.dict("os.environ")
        env.start()
        self.addCleanup(env.stop)
        os.environ["CONTRACT_RESET_SEED"] = "1"

    def test_it_is_the_first_get_dmarc_report_hook(self):
        names = [h.__name__ for h in self.hooks.registered_before["getDmarcReport"]]
        self.assertEqual(names[0], self.hook.__name__)

    def test_a_refusal_resets_first_and_keeps_its_path(self):
        for status in ("404", "422"):
            with self.subTest(status=status):
                self.start_clean.reset_mock()
                sent = report_transaction(status)
                before = json.dumps(sent, sort_keys=True)
                self.hook(sent)
                self.start_clean.assert_called_once_with(sent)
                self.assertEqual(json.dumps(sent, sort_keys=True), before)
        self.assertEqual(self.made, [])
        self.assertEqual(self.delivered, [])

    def test_nothing_happens_without_a_reset(self):
        os.environ.pop("CONTRACT_RESET_SEED")
        sent = report_transaction("200")
        self.hook(sent)
        self.assertEqual(sent["fullPath"], REPORT_PATH)
        self.start_clean.assert_not_called()
        self.assertEqual((self.made, self.delivered), ([], []))

    def test_the_success_reads_the_report_delivered_after_the_reset(self):
        sent = report_transaction("200")
        self.hook(sent)
        self.start_clean.assert_called_once_with(sent)
        self.assertEqual(sent["fullPath"], "/api/v1/admin/dmarc-reports/5")
        # The domain's own inbound pipeline checks DMARC and does not greylist.
        [pipeline] = [b for m, p, b in self.made if p == "/api/v1/admin/pipelines"]
        self.assertEqual((pipeline["domain_id"], pipeline["direction"]), (9, "inbound"))
        names = [f["name"] for f in pipeline["filters"]]
        self.assertEqual(names, ["spf_check", "dkim_verify", "dmarc_check"])
        # The report is delivered as the gateway delivers, with no credential.
        [delivery] = self.delivered
        self.assertEqual(delivery.full_url, "http://api:8080/api/v1/messages/deliver")
        self.assertIsNone(delivery.get_header("Authorization"))
        body = json.loads(delivery.data)
        postmaster = "postmaster@" + self.domain
        self.assertEqual((body["address"], body["recipients_to"]), (postmaster, [postmaster]))
        self.assertEqual(body["client_ip"], self.globals["OWN_DMARC_CLIENT"][0])
        message = email.message_from_bytes(base64.b64decode(body["raw_message"]))
        self.assertEqual(message["From"], body["sender"])
        self.assertTrue(body["sender"].endswith("@" + self.globals["OWN_DMARC_REPORTER"]))
        [report] = [part for part in message.walk() if part.get_content_type() == "text/xml"]
        xml = report.get_payload(decode=True).decode()
        self.assertIn("<domain>{}</domain>".format(self.domain), xml)
        self.assertIn("<report_id>{}</report_id>".format(self.globals["OWN_DMARC_REPORT_ID"]), xml)
        # Only after the delivery is the report looked for.
        self.assertEqual(self.made[-1][1], "/api/v1/admin/dmarc-reports?reporter=" + self.globals["OWN_DMARC_REPORTER"])

    def test_a_report_that_was_not_stored_is_an_error_not_a_pass(self):
        self.stored = False
        sent = report_transaction("200")
        with self.assertRaisesRegex(RuntimeError, "was not stored"):
            self.hook(sent)
        self.assertEqual(sent["fullPath"], REPORT_PATH)


FRAGMENT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "dnsmasq.conf")


class OwnDNS(unittest.TestCase):
    """The hooks' mail names are answered by the testbed's DNS, from the
    fragment chore contract:dns:register publishes, never by a third party's."""

    def setUp(self):
        self.globals = load_hooks().read_a_dmarc_report_delivered_after_the_reset.__globals__
        with open(FRAGMENT) as f:
            self.lines = [line.strip() for line in f
                          if line.strip() and not line.lstrip().startswith("#")]

    def txt(self, name):
        prefix = "txt-record={},".format(name)
        return [line[len(prefix):].strip('"') for line in self.lines if line.startswith(prefix)]

    def test_the_reporter_is_a_testbed_name(self):
        self.assertTrue(self.globals["OWN_DMARC_REPORTER"].endswith(".test"))
        self.assertTrue(self.globals["OWN_DMARC_CLIENT"][1].endswith("." + self.globals["OWN_DMARC_REPORTER"]))

    def test_the_reporter_passes_spf_only_from_the_client_it_delivers_as(self):
        reporter = self.globals["OWN_DMARC_REPORTER"]
        self.assertEqual(self.txt(reporter),
                         ["v=spf1 ip4:{} -all".format(self.globals["OWN_DMARC_CLIENT"][0])])

    def test_the_reporter_publishes_a_dmarc_policy(self):
        self.assertEqual(self.txt("_dmarc." + self.globals["OWN_DMARC_REPORTER"]),
                         ["v=DMARC1; p=reject"])

    def test_the_suppressed_domain_is_answered_nxdomain_by_the_testbed(self):
        domain = self.globals["OWN_SUPPRESSED_DOMAIN"]
        local = [line[len("local=/"):].rstrip("/") for line in self.lines if line.startswith("local=/")]
        self.assertTrue(any(domain == zone or domain.endswith("." + zone) for zone in local),
                        "{} is not answered by the testbed itself".format(domain))

    def test_no_name_is_forwarded_to_a_resolver_outside_the_testbed(self):
        self.assertFalse([line for line in self.lines if line.startswith("server=")])


if __name__ == "__main__":
    unittest.main()

"""secret_scan.py's own tests, on recorded runs written out by hand (#673)."""

import base64
import json
import unittest

import secret_scan

API = "http://10.99.0.20:8080"


def exchange(
    method, path, request=None, response=None, name="", encode=False, status=200
):
    entry = {
        "comment": name,
        "request": {"method": method, "url": API + path},
        "response": {"status": status, "content": {}},
    }
    if request is not None:
        entry["request"]["postData"] = {
            "mimeType": "application/json",
            "text": json.dumps(request),
        }
    if response is not None:
        text = json.dumps(response)
        if encode:
            entry["response"]["content"] = {
                "text": base64.b64encode(text.encode()).decode(),
                "encoding": "base64",
            }
        else:
            entry["response"]["content"] = {"text": text}
    return entry


def har(*entries):
    return {"log": {"entries": list(entries)}}


SIGN_IN = exchange(
    "POST",
    "/api/v1/auth/login",
    {"email": "admin@restmail.test", "password": "<redacted>"},
    {"data": {"access_token": "<redacted>", "refresh_token": "<redacted>"}},
    "login",
)
CREATE = exchange(
    "POST",
    "/api/v1/accounts",
    {"address": "dana@example.test", "password": "dana-secret-1"},
    {"data": {"id": 7, "address": "dana@example.test"}},
    "createAccount",
)
APP_PASSWORD = exchange(
    "POST",
    "/api/v1/accounts/7/credentials",
    {"name": "phone"},
    {"data": {"id": 3, "password": "rm_abcdefghijkl_" + "x" * 43}},
    "createAppPassword",
)


class SecretScanTest(unittest.TestCase):
    def test_a_clean_run_has_no_findings(self):
        listed = exchange(
            "GET",
            "/api/v1/accounts/7/credentials",
            response={"data": [{"id": 3, "name": "phone"}]},
        )
        self.assertEqual(
            secret_scan.scan(har(SIGN_IN, CREATE, APP_PASSWORD, listed)), []
        )

    def test_a_sent_password_in_any_answer_is_a_finding(self):
        echoed = exchange(
            "GET",
            "/api/v1/accounts/7",
            response={"data": {"id": 7, "password": "dana-secret-1"}},
            name="getAccount",
        )
        findings = secret_scan.scan(har(CREATE, echoed))
        self.assertEqual(len(findings), 1)
        self.assertIn("GET " + API + "/api/v1/accounts/7 [getAccount]", findings[0])
        self.assertIn("password sent in POST", findings[0])

    def test_an_issued_secret_may_appear_only_in_the_answer_that_issued_it(self):
        listed = exchange(
            "GET",
            "/api/v1/accounts/7/credentials",
            response={"data": [{"id": 3, "note": "rm_abcdefghijkl_" + "x" * 43}]},
            name="list",
        )
        findings = secret_scan.scan(har(APP_PASSWORD, listed))
        self.assertEqual(len(findings), 1)
        self.assertIn("[list] answers the password issued by", findings[0])
        self.assertIn("<secret> (59 chars) at $.data[0].note", findings[0])

    def test_a_redacted_value_is_not_judged(self):
        # vertrag redacts its whole login body, so the admin's own address reads
        # "<redacted>" in every message it sent.
        sent = exchange(
            "POST",
            "/api/v1/messages/send",
            {"from": "<redacted>", "to": ["dana@example.test"]},
            {"data": {"sender": "<redacted>", "recipients_to": ["dana@example.test"]}},
            "send",
        )
        self.assertEqual(secret_scan.scan(har(SIGN_IN, sent)), [])

    def test_the_fixture_admins_password_is_searched_for_in_the_audit_log(self):
        audit = [
            json.dumps(
                {
                    "seq": 2,
                    "action": "auth.login",
                    "details": {"password": "admin123!@"},
                }
            )
        ]
        known = [("password", "admin123!@", "vertrag.yml's auth.login")]
        findings = secret_scan.scan(har(SIGN_IN), audit, known)
        self.assertEqual(len(findings), 1)
        self.assertIn(
            "auth.login entry holds the password sent in vertrag.yml's auth.login",
            findings[0],
        )

    def test_the_audit_log_is_searched_too(self):
        audit = [
            json.dumps(
                {
                    "seq": 1,
                    "action": "account.create",
                    "details": {"password": "dana-secret-1"},
                }
            )
        ]
        findings = secret_scan.scan(har(CREATE), audit)
        self.assertEqual(len(findings), 1)
        self.assertIn(
            "the audit log's account.create entry holds the password sent in",
            findings[0],
        )
        self.assertIn("at $.details.password", findings[0])

    def test_json_escaped_secrets_are_found_in_answers_and_audit_entries(self):
        secrets = [
            'password-with-"quotes"',
            "password-with-\\slashes",
            "pässword-雪-🔐",
            "-----BEGIN PRIVATE KEY-----\nfixture-key-material\n-----END PRIVATE KEY-----\n",
        ]
        for value in secrets:
            for encoded in (False, True):
                with self.subTest(value=value, base64=encoded):
                    sent = exchange(
                        "POST",
                        "/api/v1/accounts",
                        {"private_key": value},
                        {"data": {"id": 7}},
                        name="create",
                    )
                    echoed = exchange(
                        "GET",
                        "/api/v1/accounts/7",
                        response={"data": [{"note": "prefix " + value}]},
                        name="get",
                        encode=encoded,
                    )
                    audit = [
                        json.dumps(
                            {"action": "account.create", "details": {"copied": value}}
                        )
                    ]
                    findings = secret_scan.scan(har(sent, echoed), audit)
                    self.assertEqual(len(findings), 2)
                    self.assertIn("at $.data[0].note", findings[0])
                    self.assertIn("at $.details.copied", findings[1])

    def test_escaped_issued_secrets_keep_the_single_creation_exception(self):
        value = 'issued-"secret"-雪'
        issued = exchange(
            "POST", "/api/v1/auth/2fa/enroll", response={"data": {"secret": value}}
        )
        self.assertEqual(secret_scan.scan(har(issued)), [])
        echoed = exchange("GET", "/api/v1/auth/2fa", response={"data": {"note": value}})
        findings = secret_scan.scan(har(issued, echoed))
        self.assertEqual(len(findings), 1)
        self.assertIn("secret issued by", findings[0])

    def test_json_encoded_object_keys_are_scanned_without_printing_the_secret(self):
        value = 'password-"secret"-雪'
        sent = exchange("POST", "/api/v1/accounts", {"password": value})
        echoed = exchange(
            "GET", "/api/v1/accounts/7", response={"data": {value: "copied"}}
        )
        findings = secret_scan.scan(har(sent, echoed))
        self.assertEqual(len(findings), 1)
        self.assertIn("object key", findings[0])
        self.assertNotIn(value, findings[0])

    def test_plain_text_and_json_string_answers_are_scanned(self):
        value = 'plain-"password"-雪'
        sent = exchange("POST", "/api/v1/accounts", {"password": value})
        plain = exchange("GET", "/plain")
        plain["response"]["content"] = {"text": "prefix " + value}
        quoted = exchange("GET", "/quoted", response=value)
        findings = secret_scan.scan(har(sent, plain, quoted))
        self.assertEqual(len(findings), 2)
        self.assertIn("in the text", findings[0])
        self.assertIn("at $", findings[1])

    def test_literal_escape_sequences_do_not_match_different_decoded_values(self):
        value = r"secret-with-\n-literal"
        sent = exchange("POST", "/api/v1/accounts", {"password": value})
        different = exchange(
            "GET", "/api/v1/accounts/7", response={"note": "secret-with-\n-literal"}
        )
        self.assertEqual(secret_scan.scan(har(sent, different)), [])

    def test_numeric_passwords_are_detected_in_numeric_json_or_plain_answers(self):
        value = "123456789"
        sent = exchange("POST", "/api/v1/accounts", {"password": value})
        number = exchange("GET", "/api/v1/accounts/7", response={"note": int(value)})
        plain = exchange("GET", "/plain")
        plain["response"]["content"] = {"text": value}
        self.assertEqual(len(secret_scan.scan(har(sent, number, plain))), 2)

    def test_encoded_answers_short_values_and_push_tokens(self):
        enroll = exchange(
            "POST",
            "/api/v1/auth/2fa/enroll",
            response={
                "data": {
                    "secret": "JBSWY3DPEHPK3PXP",
                    "recovery_codes": ["aaaa-bbbb-cccc"],
                }
            },
        )
        status = exchange(
            "GET",
            "/api/v1/auth/2fa",
            response={"data": {"enrolled": True, "hint": "JBSWY3DPEHPK3PXP"}},
            encode=True,
            name="status",
        )
        confirm = exchange(
            "POST",
            "/api/v1/auth/2fa/confirm",
            {"code": "123456"},
            {"data": {"code": "123456"}},
        )
        device = exchange(
            "POST",
            "/api/v1/push/devices",
            {"platform": "apns", "token": "ab" * 32},
            {"data": {"token": "ab" * 32}},
        )
        findings = secret_scan.scan(har(enroll, status, confirm, device))
        self.assertEqual(len(findings), 1)
        self.assertIn("[status] answers the secret issued by", findings[0])

    def test_refusal_details_and_api_documents_are_not_secrets(self):
        # A 422 names the field it refuses, with words such as "must not be empty":
        # neither is an issued secret, though the field is called password.
        refused = exchange(
            "POST",
            "/api/v1/auth/login",
            {"email": "a@example.test"},
            {
                "error": {
                    "code": "validation_failed",
                    "details": {"fields": {"password": "must not be empty"}},
                }
            },
            status=422,
        )
        other = exchange(
            "POST",
            "/api/v1/accounts",
            {"address": "x@example.test"},
            {"error": {"details": {"fields": {"password": "must not be empty"}}}},
            status=422,
        )
        # The description documents the example password the run also sends.
        docs = exchange(
            "GET", "/api/docs/openapi.yaml", response={"example": "dana-secret-1"}
        )
        self.assertEqual(secret_scan.scan(har(refused, other, CREATE, docs)), [])


if __name__ == "__main__":
    unittest.main()

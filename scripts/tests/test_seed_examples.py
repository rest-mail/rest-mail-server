"""seed_examples.py turns openapi.yaml's examples into go-oapifly example pairs (#625)."""

import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))

import seed_examples  # noqa: E402

SPEC = {
    "paths": {
        "/things": {
            "post": {
                "operationId": "createThing",
                "requestBody": {"content": {"application/json": {"examples": {
                    "created": {"summary": "A new thing", "value": {"name": "box"}},
                    "name_missing": {"value": {}},
                    "loose": {"value": {"name": "x"}},
                    "null_body": {"value": None},
                    "described": {"summary": "No value, only words"},
                }}}},
                "responses": {
                    "201": {"content": {"application/json": {"examples": {
                        "created": {"value": {"id": 7, "name": "<box> & co"}}}}}},
                    "422": {"$ref": "#/components/responses/Invalid"},
                },
            },
        },
        "/things/{id}": {
            "parameters": [{"name": "id", "in": "path", "example": 1,
                            "examples": {"missing": {"value": 2147483647}}}],
            "put": {
                "operationId": "putThing",
                "requestBody": {"content": {"application/json": {"example": {"name": "y"}}}},
                "responses": {
                    "200": {"content": {"application/json": {"examples": {"replaced": {"value": {}}}}}},
                    "201": {"content": {"application/json": {"examples": {"replaced": {"value": {}}}}}},
                    "404": {"content": {"application/json": {"examples": {"missing": {"value": {"error": "x"}}}}}},
                },
            },
        },
    },
    "components": {"responses": {"Invalid": {"content": {"application/json": {"examples": {
        "name_missing": {"value": {"error": {"code": "validation_failed"}}}}}}}}},
}


class SeedExamples(unittest.TestCase):
    def setUp(self):
        self.file, self.problems = seed_examples.seed(SPEC)

    def test_a_request_goes_with_the_response_of_its_name(self):
        created = self.file["createThing"]["created"]
        self.assertEqual(created["summary"], "A new thing")
        self.assertEqual(created["request"], {"body": {"name": "box"}})
        self.assertEqual(created["response"], {"status": 201, "body": {"id": 7, "name": "<box> & co"}})

    def test_the_request_of_a_422_is_meant_to_break_its_schema(self):
        refused = self.file["createThing"]["name_missing"]
        self.assertEqual(refused["request"], {"body": {}, "invalid": True})
        self.assertEqual(refused["response"]["status"], 422)

    def test_a_null_body_is_sent_as_null_and_a_valueless_example_not_at_all(self):
        self.assertEqual(self.file["createThing"]["null_body"]["request"], {"body": None})
        self.assertNotIn("described", self.file["createThing"])

    def test_a_request_no_response_names_goes_with_the_success(self):
        self.assertEqual(self.file["createThing"]["loose"]["response"], {"status": 201})

    def test_parameter_values_and_the_unnamed_example(self):
        put = self.file["putThing"]
        self.assertEqual(put["missing"]["request"], {"path": {"id": 2147483647}})
        self.assertEqual(put["missing"]["response"], {"status": 404, "body": {"error": "x"}})
        self.assertEqual(put["example"]["request"], {"body": {"name": "y"}})
        self.assertEqual(put["example"]["response"]["status"], 200)

    def test_a_name_on_two_statuses_is_reported(self):
        self.assertEqual(self.file["putThing"]["replaced"]["response"]["status"], 200)
        self.assertEqual(self.problems, ["putThing: example replaced is named on 200 and 201; the 201 is left out"])

    def test_written_as_go_oapifly_writes_it(self):
        text = seed_examples.marshal(self.file)
        self.assertTrue(text.endswith("}\n"))
        self.assertIn('"name": "\\u003cbox\\u003e \\u0026 co"', text)
        self.assertLess(text.index('"summary"'), text.index('"request"'))
        self.assertLess(text.index('"createThing"'), text.index('"putThing"'))


if __name__ == "__main__":
    unittest.main()

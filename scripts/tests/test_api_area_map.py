"""Keep API area ownership aligned with the described operations."""

import json
from pathlib import Path
import unittest


class APIAreaMap(unittest.TestCase):
    def test_checked_in_area_map_assigns_each_operation_exactly_once(self):
        root = Path(__file__).resolve().parents[2]
        areas = json.loads((root / "tests/contract/api-areas.json").read_text())
        self.assertEqual(set(areas), {str(number) for number in range(630, 653)})
        operation_ids = [operation for operations in areas.values() for operation in operations]
        self.assertEqual(len(operation_ids), len(set(operation_ids)))
        described = set(json.loads((root / "openapi-example.json").read_text()))
        self.assertTrue({"getAccountTimeZone", "setAccountTimeZone"} <= described)
        self.assertEqual(
            sorted(described - set(operation_ids)), [], "operations without an area"
        )
        self.assertEqual(
            sorted(set(operation_ids) - described), [], "areas name undescribed operations"
        )


if __name__ == "__main__":
    unittest.main()

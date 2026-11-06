import json
import unittest
from pathlib import Path

from example_pair_shards import plan


class ExamplePairShards(unittest.TestCase):
    def test_measured_tests_have_a_shorter_longest_shard(self):
        seconds = json.loads(Path(__file__).with_name("example_pair_seconds.json").read_text())["seconds"]
        names = sorted(seconds)
        shards = plan(names, 4, seconds)
        totals = [sum(seconds[name] for name in shard) for shard in shards]
        positional = [sum(seconds[name] for name in names[index::4]) for index in range(4)]
        self.assertEqual(sorted(name for shard in shards for name in shard), names)
        self.assertLess(max(totals) - min(totals), 3)
        self.assertLess(max(totals), max(positional))

    def test_new_tests_are_not_omitted_and_input_order_does_not_change_the_plan(self):
        names = ["TestSlow", "TestMedium", "TestFast", "TestNew"]
        seconds = {"TestSlow": 60, "TestMedium": 30, "TestFast": 1}
        shards = plan(names, 3, seconds)
        self.assertEqual(shards, plan(list(reversed(names)), 3, seconds))
        self.assertEqual(sorted(name for shard in shards for name in shard), sorted(names))

    def test_no_measurements_still_run_every_test_once(self):
        self.assertEqual(plan(["TestB", "TestA", "TestC"], 2, {}), [["TestA", "TestC"], ["TestB"]])
        self.assertEqual(plan(["TestA"], 4, {}), [["TestA"]])

    def test_invalid_inputs_fail_closed(self):
        for names, count in (([], 4), (["TestA"], 0), (["TestA", "TestA"], 2)):
            with self.subTest(names=names, count=count), self.assertRaises(ValueError):
                plan(names, count, {})

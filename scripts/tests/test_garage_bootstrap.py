import importlib.util
import pathlib
import unittest

spec = importlib.util.spec_from_file_location(
    "garage_bootstrap", pathlib.Path(__file__).resolve().parents[1] / "garage-bootstrap.py")
bootstrap = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bootstrap)


class FakeGarage:
    """Answers as Garage's admin API does, and records every change."""

    def __init__(self, roles=(), buckets=(), keys=()):
        self.roles, self.buckets, self.keys = list(roles), list(buckets), list(keys)
        self.changes = []

    def call(self, method, path, body=None):
        if method == "GET":
            return {
                "GetClusterLayout": {"version": 3, "roles": self.roles},
                "ListBuckets": self.buckets,
                "ListKeys": self.keys,
            }[path]
        self.changes.append((path, body))
        return {"CreateBucket": {"id": "new-bucket"}}.get(path, {})


class Bootstrap(unittest.TestCase):
    def test_a_new_node_gets_everything(self):
        garage = FakeGarage()
        bootstrap.ensure_layout(garage, [{"id": "node"}])
        bucket = bootstrap.ensure_bucket(garage, "restmail")
        bootstrap.ensure_key(garage, "GK1", "secret", bucket)
        self.assertEqual([path for path, _ in garage.changes],
                         ["UpdateClusterLayout", "ApplyClusterLayout", "CreateBucket", "ImportKey", "AllowBucketKey"])
        self.assertEqual(dict(garage.changes)["ApplyClusterLayout"], {"version": 4})
        allowed = dict(garage.changes)["AllowBucketKey"]
        self.assertEqual(allowed["bucketId"], "new-bucket")
        self.assertFalse(allowed["permissions"]["owner"])

    def test_running_again_makes_nothing_new(self):
        garage = FakeGarage(roles=[{"id": "node"}], buckets=[{"id": "b1", "globalAliases": ["restmail"]}], keys=[{"id": "GK1"}])
        bootstrap.ensure_layout(garage, [{"id": "node"}])
        bucket = bootstrap.ensure_bucket(garage, "restmail")
        bootstrap.ensure_key(garage, "GK1", "secret", bucket)
        self.assertEqual(bucket, "b1")
        self.assertEqual([path for path, _ in garage.changes], ["AllowBucketKey"])


if __name__ == "__main__":
    unittest.main()

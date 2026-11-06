"""The chart's PrometheusRule carries the Kubernetes groups of the rules file.

helm/restmail/files/alerting-rules.yml is the one copy of REST Mail's alerts
(#1579). The template reads it; this checks the rendered PrometheusRule has
every group of it, rule for rule (name, expr, for, labels, annotations), except
restmail-disk-node, which reads node-exporter series only the compose stack
scrapes. Usage: alerting-rules-test.py <rendered chart with the rule enabled>
"""
import json
import pathlib
import subprocess
import sys

COMPOSE_ONLY = {"restmail-disk-node"}

chart = pathlib.Path(__file__).resolve().parents[1]


def yq_json(expr, path):
    return json.loads(subprocess.check_output(["yq", "-o=json", expr, str(path)], text=True))


file_groups = yq_json(".groups", chart / "files/alerting-rules.yml")
rendered = yq_json('select(.kind == "PrometheusRule") | .spec.groups', sys.argv[1])

names = [g["name"] for g in file_groups]
missing = COMPOSE_ONLY - set(names)
if missing:
    raise SystemExit(f"compose-only groups {sorted(missing)} are not in the rules file; update COMPOSE_ONLY and the template")
expected = [g for g in file_groups if g["name"] not in COMPOSE_ONLY]
if not expected:
    raise SystemExit("the rules file has no group for Kubernetes")

got_names = [g["name"] for g in rendered]
want_names = [g["name"] for g in expected]
if got_names != want_names:
    raise SystemExit(f"PrometheusRule groups {got_names}, want {want_names}")
for want, got in zip(expected, rendered):
    if want != got:
        raise SystemExit(f"PrometheusRule group {want['name']} differs from files/alerting-rules.yml")

rules = sum(len(g["rules"]) for g in expected)
print(f"ok: PrometheusRule carries {len(expected)} groups, {rules} rules, of files/alerting-rules.yml")

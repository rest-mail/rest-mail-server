"""The chart's PrometheusRule carries the Kubernetes groups of the rules file.

helm/restmail/files/alerting-rules.yml is the one copy of REST Mail's alerts
(#1579). The template reads it; this checks the rendered PrometheusRule has
every group of it, rule for rule (name, expr, for, labels, annotations), except
restmail-disk-node, which reads node-exporter series only the compose stack
scrapes. Usage: alerting-rules-test.py <rendered chart> <promtool fixtures...>
"""
import json
import pathlib
import re
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


def seconds(duration):
    units = {"h": 3600, "m": 60, "s": 1}
    return sum(int(amount) * units[unit] for amount, unit in re.findall(r"(\d+)([hms])", duration))


def test_ac01_all_alerts_fire_and_recover(fixtures):
    proved = set()
    for path in fixtures:
        for scenario in yq_json(".tests", path):
            states = {}
            for check in scenario["alert_rule_test"]:
                states.setdefault(check["alertname"], []).append(
                    (seconds(check["eval_time"]), bool(check["exp_alerts"]))
                )
            for alert, events in states.items():
                for time, firing in events:
                    if firing and any(when < time and not active for when, active in events) and any(
                        when > time and not active for when, active in events
                    ):
                        proved.add(alert)
    required = {rule["alert"] for group in file_groups for rule in group["rules"]}
    if missing := required - proved:
        raise SystemExit(f"FAIL: AC01 fire-and-recover fixture missing for {', '.join(sorted(missing))}")
    print(f"ok: AC01 all {len(required)} alerts have no-fault, firing and recovery cases")


test_ac01_all_alerts_fire_and_recover(sys.argv[2:])

import json
import pathlib
import subprocess
import sys

root = pathlib.Path(__file__).resolve().parents[3]
# The plain Prometheus rules ship in monitoring/alerting-rules.yml (#1579).
documented = json.loads(
    subprocess.check_output(
        [
            "yq",
            "-o=json",
            '[.groups[] | select(.name == "restmail-dkim")]',
            str(root / "monitoring/alerting-rules.yml"),
        ],
        text=True,
    )
)
rendered = json.loads(
    subprocess.check_output(
        [
            "yq",
            "-o=json",
            'select(.kind == "PrometheusRule") | .spec.groups',
            sys.argv[1],
        ],
        text=True,
    )
)
if documented != rendered:
    raise SystemExit("DKIM chart rules differ from the restmail-dkim group in monitoring/alerting-rules.yml")
print("ok: DKIM chart rules match monitoring/alerting-rules.yml")

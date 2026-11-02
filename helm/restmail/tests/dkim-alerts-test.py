import json
import pathlib
import re
import subprocess
import sys

root = pathlib.Path(__file__).resolve().parents[3]
document = (root / "docs/dkim-rotation-alerts.md").read_text()
block = re.search(r"```yaml\n(.*?)\n```", document, re.DOTALL).group(1)
documented = json.loads(
    subprocess.check_output(["yq", "-o=json", ".groups", "-"], input=block, text=True)
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
    raise SystemExit("DKIM chart rules differ from the plain Prometheus documentation")
print("ok: DKIM chart rules match the plain Prometheus documentation")

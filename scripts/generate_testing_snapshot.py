"""Generate test snapshot schemas from the models' JSON fields.

Run from the repository root (or use go generate ./internal/api/handlers).
Fixture schemas need separate names from public API responses, and JSONB
columns must describe JSON rather than the underlying byte slice's base64.
The Go parity test detects new fields or JSON tag changes that need regeneration.
"""

from pathlib import Path
import re
import subprocess


models = {}
for source in sorted(Path("internal/db/models").glob("*.go")):
    for name, body in re.findall(
        r"type (\w+) struct \{(.*?)\n\}", source.read_text(), re.S
    ):
        models[name] = body


def fields(name):
    for line in models[name].splitlines():
        if line.strip().startswith("//") or "json:" not in line or 'json:"-"' in line:
            continue
        field, typ, *_ = line.strip().split()
        tag = re.search(r'json:"[^"]+"', line).group()
        yield field, typ, tag


pending = ["Domain", "Mailbox", "Alias", "Message", "OutboundQueue", "Pipeline", "Ban"]
used = []
while pending:
    name = pending.pop(0)
    if name in used:
        continue
    used.append(name)
    for _, typ, _ in fields(name):
        base = typ.lstrip("[]*")
        if base in models and base not in used:
            pending.append(base)

output = '''// Code generated from internal/db/models by generate_testing_snapshot.py; DO NOT EDIT.
package handlers

import (
    "encoding/json"
    "time"
    "github.com/restmail/restmail/internal/db/models"
)

'''
for prefix, optional in [("TestSnapshot", False), ("RestoreSnapshot", True)]:
    for name in used:
        output += f"type {prefix}{name} struct {{\n"
        for field, typ, tag in fields(name):
            base = typ.lstrip("[]*")
            if base in models:
                typ = typ.replace(base, prefix + base)
            elif typ == "JSONB":
                typ = "json.RawMessage"
            elif typ == "FailureCategory":
                typ = "models.FailureCategory"
            if optional and ",omitempty" not in tag:
                tag = tag[:-1] + ',omitempty"'
            output += f"    {field} {typ} `{tag}`\n"
        output += "}\n\n"

target = Path("internal/api/handlers/testing_snapshot_generated.go")
target.write_text(output)
subprocess.run(["gofmt", "-w", str(target)], check=True)

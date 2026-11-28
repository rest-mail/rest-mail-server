"""Compare the served admin API with callable methods in the pinned Tonik client."""

import argparse
import json
import re
import sys
from pathlib import Path

METHODS = frozenset(
    {"get", "put", "post", "delete", "options", "head", "patch", "trace"}
)
ADMIN = "/api/v1/admin"
TEST_ADMIN = ADMIN + "/test"
EXCLUSIONS = Path(__file__).with_name("mobile_admin_coverage_exclusions.json")

# Match Tonik's generated exports, operation imports, fields and method bodies.
API_EXPORT = re.compile(r"^export '(src/api_client/\w+\.dart)';$", re.MULTILINE)
OP_IMPORT = re.compile(
    r"^import 'package:restmail_generated/(src/operation/\w+\.dart)'\s+as (\w+);$",
    re.MULTILINE,
)
OP_FIELD = re.compile(r"^  final (\w+)\.\w+ (_\w+);$", re.MULTILINE)
OP_CALL = re.compile(r"\b[A-Za-z]\w*\(\{[^}]*\}\)\s+async =>\s*(_\w+)\(")
OP_PATH = re.compile(r"\b_path\([^)]*\) \{\s*return \[(.*?)\];", re.DOTALL)
PATH_PART = re.compile(r"\s*(?:r'([^']*)'|(\w+)\.toSimple\([^)]*\))\s*(?:,|$)")


def admin_operations(document):
    operations = {
        (method, path, operation["operationId"])
        for path, item in document["paths"].items()
        if (path == ADMIN or path.startswith(ADMIN + "/"))
        and path != TEST_ADMIN
        and not path.startswith(TEST_ADMIN + "/")
        for method, operation in item.items()
        if method in METHODS
    }
    if not operations:
        raise ValueError("OpenAPI contains no production admin operations")
    return operations


def route_key(method, path):
    # Tonik changes parameter names to Dart identifiers. The locked OpenAPI
    # separately ties each generated wire route to its exact operation ID.
    return method, re.sub(r"\{[^/{}]+\}", "{}", path)


def generated_route(source):
    method = re.search(r"\bmethod: '([A-Z]+)',", source)
    path = OP_PATH.search(source)
    if method is None or path is None:
        raise ValueError("unrecognized Tonik operation method or path")
    segments = path[1].strip()
    parts = []
    while segments:
        part = PATH_PART.match(segments)
        if part is None:
            raise ValueError(f"unrecognized Tonik path segment: {segments!r}")
        parts.append(part[1] if part[1] is not None else "{}")
        segments = segments[part.end() :].strip()
    return method[1].lower(), "/" + "/".join(parts)


def generated_routes(library):
    exports = API_EXPORT.findall((library / "restmail_generated.dart").read_text())
    operation_files = set()
    for filename in exports:
        source = (library / filename).read_text()
        imports = {alias: path for path, alias in OP_IMPORT.findall(source)}
        fields = {field: alias for alias, field in OP_FIELD.findall(source)}
        operation_files.update(
            imports[fields[field]] for field in OP_CALL.findall(source)
        )
    routes = set()
    for filename in sorted(operation_files):
        try:
            routes.add(generated_route((library / filename).read_text()))
        except ValueError as exc:
            raise ValueError(f"{filename}: {exc}") from exc
    return routes


def coverage_errors(server, mobile, exclusions):
    uncovered = server - mobile
    uncovered_ids = {op_id for _, _, op_id in uncovered}
    errors = [
        f"missing mobile method: {op_id} ({method.upper()} {path})"
        for method, path, op_id in sorted(uncovered)
        if op_id not in exclusions
    ]
    for op_id, reason in sorted(exclusions.items()):
        if not isinstance(reason, str) or not reason.strip():
            errors.append(f"exclusion for {op_id} needs a reason")
        if op_id not in uncovered_ids:
            errors.append(f"remove stale mobile coverage exclusion: {op_id}")
    return errors


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("server_openapi", type=Path)
    parser.add_argument("mobile_checkout", type=Path)
    args = parser.parse_args()

    try:
        server = admin_operations(json.loads(args.server_openapi.read_text()))
        pinned_document = args.mobile_checkout / "api_contract/openapi.json"
        pinned = admin_operations(json.loads(pinned_document.read_text()))
        library = args.mobile_checkout / "packages/restmail_generated/lib"
        routes = generated_routes(library)
        mobile = {op for op in pinned if route_key(op[0], op[1]) in routes}
        exclusions = json.loads(EXCLUSIONS.read_text())
        errors = coverage_errors(server, mobile, exclusions)
    except (OSError, ValueError, KeyError) as exc:
        print(f"mobile admin coverage: {exc}", file=sys.stderr)
        return 1

    for error in errors:
        print(error, file=sys.stderr)
    if errors:
        return 1
    print(
        f"Mobile client covers {len(server & mobile)} of {len(server)} production admin operations."
    )
    for op_id, reason in sorted(exclusions.items()):
        print(f"Excluded {op_id}: {reason}")
    return 0


if __name__ == "__main__":
    sys.exit(main())

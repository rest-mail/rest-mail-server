"""Seed openapi-example.json from the examples written in openapi.yaml (#625).

openapi.yaml carries REST Mail's examples where OpenAPI puts them: named request
bodies, named parameter values and named response bodies, paired by name (#386).
The generated description takes them from openapi-example.json instead, beside
the document, in go-oapifly's example file format (antimatter-studios/go-oapifly#20):
by operationId, then by example name, a request and the response it provokes.

  seed_examples.py [openapi.yaml] [openapi-example.json]

Run once, so nothing written so far is lost; from then on the file is kept with
`oapifly examples sync` and `check`. It is written as go-oapifly's
MarshalExamples writes it, so a later sync changes only what it adds. Each pair:

- takes the request body, path, query and header values given under its name,
  and the response of the status whose example has its name;
- a request example no response is named after pairs with the operation's
  success status, the lowest 2xx, as vertrag pairs a named request with the
  unnamed responses;
- a response example no request is named after pairs with an empty request;
- an operation's single `example` (unnamed) is the pair `example`;
- the request of a 400 or 422 pair is marked `invalid`, meant to break its
  schema. One refused for a rule the schema does not state is a finding of
  `oapifly examples check` (tests/contract/example-findings.txt).

A response example of a name another status already has is reported and left
out: a name is one exchange. The output is sorted and indented, so running it
twice gives the same bytes.
"""

import json
import os
import sys

import yaml

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
METHODS = ("get", "put", "post", "delete", "options", "head", "patch", "trace")
UNNAMED = "example"
# NO_VALUE is an Example Object with no value (an externalValue, or a summary only),
# told apart from one whose value is JSON null.
NO_VALUE = object()


def resolve(spec, node):
    """Follow a local $ref, as many times as it takes."""
    while isinstance(node, dict) and "$ref" in node:
        ref = node["$ref"]
        if not ref.startswith("#/"):
            raise ValueError("only local references are followed: " + ref)
        target = spec
        for part in ref[2:].split("/"):
            target = target[part.replace("~1", "/").replace("~0", "~")]
        node = target
    return node


def named_values(spec, holder):
    """The examples a Media Type or Parameter Object gives: (name, value, summary)."""
    holder = resolve(spec, holder) or {}
    if "example" in holder:
        return [(UNNAMED, holder["example"], "")]
    out = []
    for name, example in (holder.get("examples") or {}).items():
        example = resolve(spec, example) or {}
        if "value" in example:
            out.append((name, example["value"], example.get("summary", "")))
        elif "dataValue" in example:
            out.append((name, example["dataValue"], example.get("summary", "")))
        else:
            out.append((name, NO_VALUE, example.get("summary", "")))
    return out


def media_examples(spec, content):
    """(name, media type, value, summary) for each example of a Content Object, the
    media types in order."""
    out = []
    for media in sorted(content or {}):
        for name, value, summary in named_values(spec, content[media]):
            out.append((name, media, value, summary))
    return out


def first_media(content):
    return sorted(content)[0] if content else ""


def seed(spec):
    """The example file for spec, and the problems it could not place."""
    file, problems = {}, []
    for path, item in sorted((spec.get("paths") or {}).items()):
        item = resolve(spec, item)
        shared = item.get("parameters") or []
        for method in METHODS:
            op = item.get(method)
            if not isinstance(op, dict) or not op.get("operationId"):
                continue
            op_id = op["operationId"]
            pairs = {}

            def pair(name):
                return pairs.setdefault(name, {"request": {}, "response": {}})

            # Requests: the body, then each parameter's values, by name.
            body = resolve(spec, op.get("requestBody")) or {}
            content = body.get("content") or {}
            for name, media, value, summary in media_examples(spec, content):
                if value is NO_VALUE:
                    continue
                p = pair(name)
                p["request"]["body"] = value
                if media != first_media(content):
                    p["request"]["mediaType"] = media
                if summary:
                    p.setdefault("summary", summary)
            for parameter in shared + (op.get("parameters") or []):
                parameter = resolve(spec, parameter)
                where = parameter.get("in")
                if where not in ("path", "query", "header"):
                    continue
                # Only the named values: a parameter's single example is its
                # default value, which every request is sent with, and names no
                # exchange.
                for name, value, summary in named_values(spec, {"examples": parameter.get("examples") or {}}):
                    if value is NO_VALUE:
                        continue
                    pair(name)["request"].setdefault(where, {})[parameter["name"]] = value

            # Responses: each named body goes with the request of its name.
            responses = op.get("responses") or {}
            statuses = sorted(s for s in responses if s.isdigit())
            success = next((s for s in statuses if s.startswith("2")), statuses[0] if statuses else "")
            for status in statuses:
                response = resolve(spec, responses[status])
                content = response.get("content") or {}
                for name, media, value, summary in media_examples(spec, content):
                    p = pair(name)
                    if p["response"].get("status") not in (None, int(status)):
                        problems.append("%s: example %s is named on %s and %s; the %s is left out"
                                        % (op_id, name, p["response"]["status"], status, status))
                        continue
                    p["response"]["status"] = int(status)
                    if value is not NO_VALUE:
                        p["response"]["body"] = value
                    if media != first_media(content):
                        p["response"]["mediaType"] = media
                    if summary:
                        p.setdefault("summary", summary)

            for name, p in pairs.items():
                if "status" not in p["response"]:
                    if not success:
                        problems.append("%s: example %s has no response to pair with" % (op_id, name))
                        continue
                    p["response"]["status"] = int(success)
                # The request of a 400 or 422 pair is meant to break its schema.
                if p["response"]["status"] in (400, 422) and "body" in p["request"]:
                    p["request"]["invalid"] = True
                if name == UNNAMED and "summary" not in p:
                    p["summary"] = "The operation's example"
            if pairs:
                file[op_id] = {name: ordered(pairs[name]) for name in sorted(pairs) if "status" in pairs[name]["response"]}
    return file, problems


# The order go-oapifly's ExamplePair, ExampleRequest and ExampleResponse declare their
# fields in, which is the order MarshalExamples writes them.
PAIR_FIELDS = ("summary", "description", "request", "response")
REQUEST_FIELDS = ("path", "query", "header", "body", "mediaType", "invalid")
RESPONSE_FIELDS = ("status", "body", "mediaType")


def ordered(pair):
    """pair with its fields in go-oapifly's order."""
    out = {k: pair[k] for k in PAIR_FIELDS if k in pair}
    out["request"] = {k: pair["request"][k] for k in REQUEST_FIELDS if k in pair["request"]}
    out["response"] = {k: pair["response"][k] for k in RESPONSE_FIELDS if k in pair["response"]}
    for where in ("path", "query", "header"):
        if where in out["request"]:
            out["request"][where] = dict(sorted(out["request"][where].items()))
    return out


def marshal(file):
    """The file as go-oapifly's MarshalExamples writes it: operations and names sorted,
    indented by two spaces, <, > and & escaped as encoding/json escapes them."""
    text = json.dumps(dict(sorted(file.items())), indent=2, ensure_ascii=False)
    for char, escaped in (("&", "\\u0026"), ("<", "\\u003c"), (">", "\\u003e"),
                          ("\u2028", "\\u2028"), ("\u2029", "\\u2029")):
        text = text.replace(char, escaped)
    return text + "\n"


def main(argv):
    source = argv[1] if len(argv) > 1 else os.path.join(ROOT, "openapi.yaml")
    target = argv[2] if len(argv) > 2 else os.path.join(ROOT, "openapi-example.json")
    with open(source) as f:
        spec = yaml.safe_load(f)
    file, problems = seed(spec)
    with open(target, "w") as f:
        f.write(marshal(file))
    count = sum(len(pairs) for pairs in file.values())
    print("%d example pairs for %d operations written to %s" % (count, len(file), target))
    for problem in problems:
        print("left out: " + problem, file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))

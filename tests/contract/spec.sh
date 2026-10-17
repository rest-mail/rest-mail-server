#!/bin/sh
# Writes the spec vertrag reads, .cache/contract/openapi.yaml: openapi.yaml
# with the search parameter "has:attachment" named "has%3Aattachment".
#
# vertrag builds a URI template from each operation's parameters, and a ':' is
# not allowed in a template's variable name (RFC 6570), so the real name stops
# the whole spec from being read. Percent-encoded it is a valid name, and the
# request on the wire is the same: has%3Aattachment=true, which the server
# reads as has:attachment. Drop this once vertrag encodes such names itself.
set -eu
root="$(cd "$(dirname "$0")/../.." && pwd)"
mkdir -p "$root/.cache/contract"
sed 's/- name: "has:attachment"/- name: "has%3Aattachment"/' "$root/openapi.yaml" > "$root/.cache/contract/openapi.yaml"
if grep -q 'name: "has:attachment"' "$root/.cache/contract/openapi.yaml"; then
  echo "spec.sh: has:attachment is still in the spec; the substitution no longer matches" >&2
  exit 1
fi

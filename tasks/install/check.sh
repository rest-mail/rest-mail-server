#!/usr/bin/env bash
# Is a rest-mail install actually there, and actually answering?
#
#   NAME=restmail PROJECT=~/infra tasks/install/check.sh     # installed by Pulumi
#   NAME=restmail NAMESPACE=rest-mail tasks/install/check.sh # installed by Helm
#
# Called by `chore install:check`. Pulumi mode when PROJECT holds a Pulumi.yaml,
# Helm mode otherwise.
#
# Four checks, in order, stopping at the first that fails — each failure says
# what to do next, which is the whole reason they are separate:
#
#   1. it is in the stack (or Helm knows the release)
#   2. it was given an admin password
#   3. Helm finished installing it
#   4. the server answers over its public API, as rest-mail
#
# The fourth is the one that matters, and the reason the others are not enough:
# the printed Pulumi resource sets skipAwait, so `pulumi up` succeeds as soon as
# the release exists — before, and whether or not, a single pod starts. Only
# asking the server proves somebody could sign in. It asks the way every client
# does, over HTTPS by name, needing no access to the cluster.
#
# The admin password is never read. Checks 2 look for its KEY: in the stack's
# config listing, which shows secrets without decrypting them, or in the
# release's values, which are tested without being printed.
set -uo pipefail

: "${NAME:?}"
PROJECT=${PROJECT:-}
STACK=${STACK:-}
NAMESPACE=${NAMESPACE:-rest-mail}
URL=${URL:-}

stack_args=()
[ -n "$STACK" ] && stack_args=(--stack "$STACK")

step() { printf '  %-28s' "$1"; }
ok() { echo "ok${1:+  ($1)}"; }
fail() {
  echo "NO"
  echo
  printf '%s\n' "$@" >&2
  exit 1
}

for tool in jq curl; do
  command -v "$tool" >/dev/null || { echo "$tool is not installed; this check needs it" >&2; exit 1; }
done

if [ -n "$PROJECT" ] && { [ -f "$PROJECT/Pulumi.yaml" ] || [ -f "$PROJECT/Pulumi.yml" ]; }; then
  stack=$(pulumi -C "$PROJECT" stack --show-name "${stack_args[@]}" 2>/dev/null) ||
    { echo "no stack selected in $PROJECT: pass --stack, or run \`pulumi stack select\` there" >&2; exit 1; }
  echo "Checking \"$NAME\" in Pulumi project $PROJECT, stack $stack"

  step "in the stack"
  state=$(pulumi -C "$PROJECT" stack export "${stack_args[@]}" 2>/dev/null) ||
    fail "Could not read the stack's state."
  # By logical name or release name: the printed resource makes them the same,
  # but a hand-written one need not.
  release=$(jq -c --arg n "$NAME" '
    [.deployment.resources[]?
      | select(.type == "kubernetes:helm.sh/v3:Release")
      | select((.urn | endswith("::" + $n)) or .outputs.name == $n)
    ] | first // empty' <<<"$state")
  [ -n "$release" ] || fail \
    "There is no Helm release called \"$NAME\" in stack $stack." \
    "Paste the resource \`chore install:pulumi\` printed into the project, then run \`pulumi up\`." \
    "(A resource that is pasted but not yet deployed is not in the stack.)"
  ok

  step "admin password set"
  pulumi -C "$PROJECT" config --json "${stack_args[@]}" 2>/dev/null |
    jq -e 'has("restmail:bootstrapAdminPassword")' >/dev/null ||
    fail "Stack $stack has no restmail:bootstrapAdminPassword." \
      "Run \`chore install:pulumi\` again, or: pulumi config set --secret restmail:bootstrapAdminPassword"
  ok "encrypted in the stack"

  status=$(jq -r '.outputs.status.status // "unknown"' <<<"$release")
  host=$(jq -r '.outputs.values.mailserver.hostname // empty' <<<"$release")
else
  command -v helm >/dev/null || { echo "helm is not installed; for a Pulumi install pass --project" >&2; exit 1; }
  echo "Checking Helm release \"$NAME\" in namespace $NAMESPACE"

  step "release exists"
  info=$(helm status "$NAME" -n "$NAMESPACE" -o json 2>/dev/null) || fail \
    "Helm has no release \"$NAME\" in namespace $NAMESPACE on the cluster your kubeconfig points at." \
    "Installed with Pulumi? Pass --project with the project's directory."
  ok

  step "admin password set"
  values=$(helm get values "$NAME" -n "$NAMESPACE" -o json 2>/dev/null) || values='{}'
  jq -e '(.api.bootstrapAdminPassword // "") | length > 0' >/dev/null <<<"$values" ||
    fail "The release was installed without an admin password; nobody will be able to sign in." \
      "Upgrade it with one: helm upgrade $NAME ... --reuse-values --set api.bootstrapAdminPassword=..."
  ok "in the release's values"

  status=$(jq -r '.info.status // "unknown"' <<<"$info")
  host=$(jq -r '.mailserver.hostname // empty' <<<"$values")
fi

step "helm finished"
[ "$status" = deployed ] || fail \
  "Helm reports the release as \"$status\", not \"deployed\"." \
  "See why: helm status $NAME -n $NAMESPACE"
ok "$status"

step "server answers"
if [ -z "$URL" ]; then
  [ -n "$host" ] || fail "The release has no mailserver.hostname to ask. Pass --url."
  URL="https://$host"
fi
body=$(curl -sS --max-time 15 -H 'Accept: application/json' "$URL/api/v1/server" 2>&1)
code=$?
case $code in
0) ;;
6) fail "$URL: the name does not resolve. Point DNS at the cluster's ingress, then check again." \
     "The install itself is in place; this is the last step." ;;
7) fail "$URL: nothing is listening. Is there an ingress for the API, and does DNS point at it?" ;;
28) fail "$URL: no answer within 15 seconds. The pods may still be starting:" \
      "  kubectl get pods -n $NAMESPACE" ;;
35 | 51 | 58 | 60) fail "$URL: the TLS certificate is not trusted yet. If cert-manager is issuing it, give it a few minutes." \
      "Every pod also waits for that Secret, so nothing will answer until it exists." ;;
*) fail "$URL: could not connect (curl exit $code): $body" ;;
esac
product=$(jq -r '.data.product // empty' <<<"$body" 2>/dev/null)
[ "$product" = rest-mail ] || fail \
  "$URL answered, but not as rest-mail. Something else is serving that address:" "$(head -c 200 <<<"$body")"
version=$(jq -r '.data.version' <<<"$body")
if [ "$(jq -r '.data.claimed' <<<"$body")" = true ]; then
  ok "rest-mail $version, already has an admin"
else
  ok "rest-mail $version, waiting for its admin"
  echo
  echo "Sign in to the rest-mail app at $URL as \"admin\" with the password you made."
fi

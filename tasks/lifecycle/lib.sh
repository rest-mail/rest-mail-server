# shellcheck shell=bash
# Shared by every lifecycle task (tasks/{install,upgrade,uninstall,server,
# backup,dns,data,password,sessions}.yml). Sourced, never run.
#
# Every task that acts on a deployed server finds it the same way: a Helm
# release, NAME in NAMESPACE, on the cluster the kubeconfig points at. That
# holds for a Pulumi install too — `kubernetes:helm.sh/v3:Release` installs
# through Helm, so the release is in the cluster whichever tool put it there —
# which is why only install, upgrade and uninstall have a Pulumi variant.
#
# Nothing here prints a secret. Values that carry one are read into variables,
# passed through files made 0600 or through stdin, and removed on exit.

# Sourced once, however many scripts in a chain source it.
[ -n "${_RESTMAIL_LIB:-}" ] && return 0
_RESTMAIL_LIB=1

NAME=${NAME:-restmail}
NAMESPACE=${NAMESPACE:-rest-mail}
CHART=${CHART:-oci://ghcr.io/rest-mail/charts/restmail}

# Exit statuses shared by the scripts, so a task's caller can tell a check that
# failed from a task that could not run.
# EXIT_OUTSIDE is server:verify's "the server is fine in-cluster, and the way
# in from outside is not" — DNS, ingress or certificate, none of which an
# upgrade changes, so an upgrade does not offer a rollback for it.
# shellcheck disable=SC2034 # read by the scripts that source this
readonly EXIT_FAILED=1 EXIT_REFUSED=2 EXIT_OUTSIDE=3

say() { printf '%s\n' "$@"; }
err() { printf '%s\n' "$@" >&2; }
die() {
  err "$@"
  exit "$EXIT_FAILED"
}
refuse() {
  err "$@"
  exit "$EXIT_REFUSED"
}

need() {
  local tool
  for tool in "$@"; do
    command -v "$tool" >/dev/null || die "$tool is not installed, and this needs it."
  done
}

# A selector for one component of this release.
component() { printf 'app.kubernetes.io/instance=%s,app.kubernetes.io/component=%s' "$NAME" "$1"; }

# kubectl and helm, always in the release's namespace.
k() { kubectl -n "$NAMESPACE" "$@"; }
h() { helm -n "$NAMESPACE" "$@"; }

release_exists() { h status "$NAME" >/dev/null 2>&1; }

need_release() {
  kubectl version --request-timeout=10s >/dev/null 2>&1 || refuse \
    "No cluster reachable through your kubeconfig ($(kubectl config current-context 2>/dev/null || echo 'no context'))." \
    "Check KUBECONFIG, and that you can reach the cluster: kubectl get nodes"
  release_exists || refuse \
    "There is no rest-mail release \"$NAME\" in namespace $NAMESPACE on the cluster" \
    "your kubeconfig points at ($(kubectl config current-context 2>/dev/null || echo 'no context'))." \
    "Installed under another name? Pass --name and --namespace."
}

# The values the release was installed with — what an upgrade must carry
# forward. JSON. Holds the bootstrap password if one was given, so it goes to
# a variable or a 0600 file, never to the screen.
release_values() { h get values "$NAME" -o json 2>/dev/null || echo '{}'; }

# Chart version and app version, from Helm's own record of the release.
release_chart_version() {
  h list --filter "^${NAME}\$" -o json | jq -r '.[0].chart // "" | sub("^restmail-"; "")'
}
release_app_version() { h list --filter "^${NAME}\$" -o json | jq -r '.[0].app_version // ""'; }
release_revision() { h list --filter "^${NAME}\$" -o json | jq -r '.[0].revision // ""'; }

# The newest published chart, or what --version named.
latest_chart_version() { helm show chart "$CHART" 2>/dev/null | awk '$1 == "version:" { print $2 }'; }

# The API's Deployment, by label rather than by assumed name.
api_deployment() { k get deploy -l "$(component api)" -o name 2>/dev/null | head -n1; }

# The Deployments server:offline stops. Postgres stays up: backups, password
# resets and session revocation all need the database while nothing else runs.
serving_deployments() {
  k get deploy -l "app.kubernetes.io/instance=$NAME" -o name 2>/dev/null
}

# shellcheck disable=SC2034 # read by the scripts that source this
readonly OFFLINE_ANNOTATION=rest-mail.com/replicas-before-offline
is_offline() {
  local d
  for d in $(serving_deployments); do
    [ -n "$(k get "$d" -o jsonpath="{.metadata.annotations.rest-mail\.com/replicas-before-offline}")" ] && return 0
  done
  return 1
}

# A temporary file only this user can read, removed when the script exits.
_tmpfiles=()
private_tmp() {
  local f
  f=$(umask 077 && mktemp)
  _tmpfiles+=("$f")
  printf '%s' "$f"
}
_cleanup_hooks=()
on_exit() { _cleanup_hooks+=("$1"); }
_cleanup() {
  local hook
  for hook in "${_cleanup_hooks[@]}"; do eval "$hook" || true; done
  rm -f "${_tmpfiles[@]}"
}
trap _cleanup EXIT

# A password: 32 characters of base64url, so it survives being carried
# through a shell, a URL or a YAML file unquoted, and comfortably clears the
# server's 16-character minimum.
new_password() { openssl rand -base64 33 | tr '+/' '-_' | tr -d '=\n' | cut -c1-32; }

# Ask on the terminal itself, not stdin: `yes |` cannot answer it, and with no
# terminal at all (a pipeline) it fails closed.
ask_tty() { # ask_tty PROMPT -> the answer on stdout
  local answer
  { printf '%s' "$1" >/dev/tty && IFS= read -r answer </dev/tty; } 2>/dev/null ||
    refuse "This needs an answer from a person at a terminal, and there is none."
  printf '%s' "$answer"
}

yes_no() { # yes_no PROMPT -> status 0 for yes
  case "$(ask_tty "$1 [y/N] ")" in y | Y | yes | YES) return 0 ;; *) return 1 ;; esac
}

# The gate on destroying data. A code made here and shown only on the
# terminal; the answer read only from the terminal. --yes answers chore's
# confirms and knows nothing of this; `echo CODE |` feeds stdin, which this
# never reads. AUTOMATE=1 — set only by a flag that says what it does — is the
# one way past it, so automating it is possible but never an accident.
typed_code_gate() { # typed_code_gate WHAT
  if [ "${AUTOMATE:-}" = 1 ]; then
    err "Not asking for a code: --i-mean-it was given."
    return 0
  fi
  local code answer
  code=$(LC_ALL=C tr -dc 'A-HJ-NP-Z2-9' </dev/urandom | head -c 6)
  { printf '\n%s\n\nType %s to go ahead, anything else to stop: ' "$1" "$code" >/dev/tty; } 2>/dev/null ||
    refuse "This destroys data, so it needs a person at a terminal to type a code, and there is none." \
      "To do it from a script on purpose, pass --i-mean-it."
  IFS= read -r answer </dev/tty
  [ "$answer" = "$code" ] || refuse "Stopped. Nothing was changed."
}

# Run `restmail-api ARGS...` in a pod of its own, built from the API's pod
# template: same image, same configuration, same Secrets, same network policy.
# It does not need the API to be running — which is the point, since password
# resets and session revocation are what you do while it is offline.
#
# INPUT, when set, is given to the command on stdin, through a Secret that
# exists only as long as the pod does: never on a command line, never in the
# pod spec.
#
# Prints the command's stdout (one JSON object); on failure prints its error
# object to stderr and returns its exit status.
#
# It runs in a subshell of its own, with its own exit trap, so the pod and the
# Secret go however it ends — callers take its output with $(...), and a
# subshell never runs its parent's trap.
operator() (
  local deploy pod input_secret='' spec status log
  deploy=$(api_deployment)
  [ -n "$deploy" ] || die "No API Deployment found for release $NAME in $NAMESPACE."
  pod="$NAME-operator-$(LC_ALL=C tr -dc 'a-z0-9' </dev/urandom | head -c 6)"

  local cmd='exec restmail-api "$@"'
  if [ -n "${INPUT:-}" ]; then
    input_secret="$pod-input"
    cmd='exec restmail-api "$@" < /operator-input/stdin'
  fi

  # The template, minus what an operator command must not have: the
  # attachments volume (one node at a time, and the API may hold it), and the
  # liveness probe. The readiness probe stays, and never passes — nothing in
  # this pod listens — so the API's Service never sends it a request although
  # it carries the API's labels, which the network policy needs to let it
  # reach the database.
  # The command's arguments go in as a JSON array: as jq positional arguments,
  # one like --username would be taken for an option of jq's own.
  local argv
  argv=$(printf '%s\n' "$@" | jq -R . | jq -sc .)
  spec=$(k get "$deploy" -o json | jq --arg pod "$pod" --arg cmd "$cmd" --arg secret "${input_secret:-}" \
    --argjson argv "$argv" '
    .spec.template as $t
    | ($t.spec.volumes // [] | map(select(.persistentVolumeClaim | not))) as $vols
    | ($vols | map(.name)) as $keep
    | {
        apiVersion: "v1", kind: "Pod",
        metadata: { name: $pod, labels: ($t.metadata.labels + {"rest-mail.com/operator": "true"}) },
        spec: ($t.spec + {
          restartPolicy: "Never",
          volumes: ($vols + (if $secret != "" then [{name: "operator-input", secret: {secretName: $secret}}] else [] end)),
          containers: [ $t.spec.containers[0]
            | del(.livenessProbe, .startupProbe)
            | .command = ["/bin/sh", "-c", $cmd, "restmail-api"]
            | .args = $argv
            | .volumeMounts = ((.volumeMounts // [] | map(select(.name as $n | $keep | index($n))))
                + (if $secret != "" then [{name: "operator-input", mountPath: "/operator-input", readOnly: true}] else [] end))
          ]
        })
      }')

  # shellcheck disable=SC2064 # the names are fixed now, on purpose
  trap "k delete pod '$pod' --ignore-not-found --wait=false >/dev/null 2>&1
        [ -z '$input_secret' ] || k delete secret '$input_secret' --ignore-not-found >/dev/null 2>&1" EXIT
  if [ -n "${input_secret:-}" ]; then
    printf '%s\n' "$INPUT" | k create secret generic "$input_secret" --from-file=stdin=/dev/stdin >/dev/null ||
      die "Could not create the Secret that carries the command's input."
  fi
  printf '%s' "$spec" | k create -f - >/dev/null || die "Could not start the operator pod."

  # Done means a terminated container, successful or not.
  if ! k wait "pod/$pod" --for=jsonpath='{.status.phase}'=Succeeded --timeout=180s >/dev/null 2>&1; then
    [ "$(k get pod "$pod" -o jsonpath='{.status.phase}' 2>/dev/null)" = Failed ] ||
      die "The operator pod did not finish within three minutes:" "$(k describe pod "$pod" | tail -n 15)"
  fi
  status=$(k get pod "$pod" -o jsonpath='{.status.containerStatuses[0].state.terminated.exitCode}')
  # A pod's log is stdout and stderr merged. The command writes its result, or
  # its error, last and as one line; anything before it is not the answer.
  log=$(k logs "$pod" 2>/dev/null | tail -n 1)
  if [ "$status" = 0 ]; then
    printf '%s\n' "$log"
    return 0
  fi
  printf '%s\n' "$log" >&2
  return "${status:-1}"
)

# The message from an operator command's error object, or the raw text.
operator_error() { jq -r '.error.message // empty' <<<"$1" 2>/dev/null || printf '%s' "$1"; }

# Before an upgrade: is there a backup from the last day? backup:create
# records when it ran on the API's Deployment, so the answer is the cluster's,
# whichever machine took it. Offers to take one; returns non-zero to stop.
backup_before_changing() {
  local last age
  last=$(k get "$(api_deployment)" -o jsonpath='{.metadata.annotations.rest-mail\.com/last-backup}' 2>/dev/null)
  if [ -n "$last" ]; then
    age=$(( ($(date -u +%s) - $(date -u -d "$last" +%s 2>/dev/null || echo 0)) / 3600 ))
    if [ "$age" -lt 24 ]; then
      say "Last backup: $last ($age hours ago)."
      return 0
    fi
  fi
  say "No backup in the last day (last: ${last:-never})."
  if yes_no "Take one now with chore backup:create?"; then
    "$(dirname "${BASH_SOURCE[0]}")/backup.sh" create || return 1
    return 0
  fi
  yes_no "Go ahead without one?"
}

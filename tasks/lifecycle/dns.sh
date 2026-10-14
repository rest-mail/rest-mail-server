#!/usr/bin/env bash
# chore dns:show / chore dns:check
#
# show: every record each domain on the server needs — MX, SPF, DMARC, DKIM,
# the mail host's address and its reverse. The DKIM record can only come from
# the server, which made the key; the address from wherever the server is
# exposed, which the server does not know about itself.
#
# check: the same list, compared with what the world sees, through a public
# resolver rather than this machine's (which may know better than the
# internet does).
#
# A perfectly healthy install delivers nothing to Gmail without these, so this
# is the likeliest reason one that passes server:verify still "does not work".
#
#   dns.sh show|check
set -uo pipefail
here=$(dirname "$0")
# shellcheck source=lib.sh
. "$here/lib.sh"

need kubectl helm jq
mode=${1:-}
need_release

host=$(release_values | jq -r '.mailserver.hostname // empty')
[ -n "$host" ] || die "The release has no mailserver.hostname."

# The address mail arrives at: the SMTP gateway's load balancer, if it has one.
ip=$(k get svc -l "$(component smtp-gateway)" -o json 2>/dev/null |
  jq -r '[.items[].status.loadBalancer.ingress[]? | .ip // empty][0] // empty')

args=(dns-records --mail-host "$host")
[ -n "$ip" ] && args+=(--ip "$ip")
if ! records=$(operator "${args[@]}" 2>&1); then
  die "Could not read the domains: $(operator_error "$records")"
fi
[ "$(jq '.domains | length' <<<"$records")" -gt 0 ] || {
  say "The server has no domains yet. Add one in the rest-mail app, then run this again."
  exit 0
}
[ -n "$ip" ] || say "The SMTP gateway has no load-balancer address, so the A and PTR records are left out:" \
  "publish $host's address wherever the server is reachable." ""

case "$mode" in
show)
  # Headings as they are; records in columns, the name column as wide as the
  # longest name across every domain, so the values line up.
  width=$(jq '[.domains[].records[].name | length] | max' <<<"$records")
  jq -r --argjson w "$width" '.domains[] |
    "\(.domain)" + (if .problem then "    (\(.problem))" else "" end),
    (.records[] | "  \(.type + "     " | .[0:5]) \(.name + (" " * ($w - (.name | length))))  "
      + (if .type == "MX" then "\(.priority) " else "" end) + .value
      + (if .type == "PTR" then "    (set by whoever owns the address, not in your zone)" else "" end)),
    ""' <<<"$records"
  ;;
check)
  need dig
  resolver=${RESOLVER:-1.1.1.1}
  failures=0
  while IFS=$'\t' read -r type name value; do
    case $type in
    MX) got=$(dig +short @"$resolver" MX "$name" | awk '{ print $2 }' | sed 's/\.$//') ;;
    PTR) got=$(dig +short @"$resolver" -x "$name" | sed 's/\.$//') ;;
    TXT) got=$(dig +short @"$resolver" TXT "$name" | sed 's/" "//g; s/^"//; s/"$//') ;;
    *) got=$(dig +short @"$resolver" "$type" "$name") ;;
    esac
    if grep -qxF -- "$value" <<<"$got"; then
      printf '  ok   %-5s %s\n' "$type" "$name"
    else
      printf '  NO   %-5s %s\n         want: %s\n         have: %s\n' "$type" "$name" "$value" "${got:-(nothing)}"
      failures=$((failures + 1))
    fi
  done < <(jq -r '.domains[].records[] | [.type, .name, .value] | @tsv' <<<"$records")
  say ""
  if [ "$failures" -gt 0 ]; then
    say "$failures records are missing or different, as $resolver sees them. chore dns:show lists them all." \
      "A record just published can take a while to be seen."
    exit "$EXIT_FAILED"
  fi
  say "Everything is published."
  ;;
*) die "usage: dns.sh show|check" ;;
esac

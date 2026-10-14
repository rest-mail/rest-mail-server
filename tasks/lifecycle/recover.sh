#!/usr/bin/env bash
# chore password:reset [--username NAME] / chore sessions:revoke
#
# Both run `restmail-api` in a pod of the API's own (lib.sh's operator), so
# neither needs mail, DNS, a certificate, a working sign-in, or even the API to
# be running: they are what you do while it is offline.
#
#   recover.sh password    a new password for USERNAME, shown once
#   recover.sh sessions    sign everybody out
set -uo pipefail
here=$(dirname "$0")
# shellcheck source=lib.sh
. "$here/lib.sh"

need kubectl helm jq openssl
need_release

case "${1:-}" in
password)
  username=${USERNAME:-admin}
  [ -t 1 ] || refuse "The new password is shown once, on the terminal, and there is none."
  password=$(new_password)
  if ! out=$(INPUT=$password operator reset-password --username "$username" 2>&1); then
    die "The reset failed: $(operator_error "$out")"
  fi
  cat <<EOF

  New password for "$username" — this is the only time it is shown:

      $password

  Sign in with it in the rest-mail app. You will choose your own password and
  enroll a new authenticator before anything else works; the old one is gone.
  Every session "$username" had has ended.

EOF
  ;;
sessions)
  if ! is_offline; then
    say "The server is online, so a client may refresh in the moment between the two halves" \
      "of this. It is still worth doing; it is airtight with chore server:offline first."
  fi
  if ! out=$(operator revoke-sessions 2>&1); then
    die "Revoking failed: $(operator_error "$out")"
  fi
  say "" "Signed everybody out: $(jq -r .refresh_tokens <<<"$out") refresh tokens revoked," \
    "$(jq -r .admins <<<"$out") admins' sessions ended at once." \
    "A mail user's access token carries on until it expires — 15 minutes by default —" \
    "but cannot be renewed."
  ;;
*) die "usage: recover.sh password|sessions" ;;
esac

#!/usr/bin/env bash
# Sourced by pulumi.sh. Only the file path and public APNs identifiers arrive
# through the environment. Key contents go directly from the file to stdin.

configure_apns() {
  local file=${APNS_KEY_FILE:-} key_id=${APNS_KEY_ID:-} team_id=${APNS_TEAM_ID:-}
  local topic=${APNS_TOPIC:-} configured='' requested=''
  [ -n "$file$key_id$team_id$topic" ] && requested=1
  if [ -n "$stack" ] && config_has apnsPrivateKey; then configured=1; fi
  [ -n "$requested$configured" ] || return 0

  [ "$(runtime)" = nodejs ] || refuse \
    "APNs key configuration currently needs the TypeScript RestMail component (runtime: nodejs)."
  [ -n "$stack" ] || refuse \
    "Configure the APNs encrypted secret where the Pulumi stack can be read. Pipeline integration is a separate step."

  if [ -n "$file" ]; then
    case "$file" in
      '~/'*) file="$HOME/${file#\~/}" ;;
      /*) ;;
      *) file="${USER_WORKING_DIR:-$PWD}/$file" ;;
    esac
    [ -f "$file" ] && [ -r "$file" ] && [ -s "$file" ] || refuse \
      "--apns-key-file must name a readable, nonempty APNs .p8 file."
  elif [ -z "$configured" ]; then
    refuse "APNs identifiers need --apns-key-file or an existing encrypted $NAME:apnsPrivateKey."
  fi
  if [ -z "$file" ] && ! p config --json 2>/dev/null |
      jq -e --arg k "$NAME:apnsPrivateKey" '.[$k].secret == true' >/dev/null; then
    refuse "$NAME:apnsPrivateKey is not encrypted. Set it again with --apns-key-file."
  fi

  [ -n "$key_id" ] || key_id=$(config_get apnsKeyId)
  [ -n "$team_id" ] || team_id=$(config_get apnsTeamId)
  [ -n "$topic" ] || topic=$(config_get apnsTopic)
  topic=${topic:-com.antimatterstudios.restmail}
  [ -n "$key_id" ] && [ -n "$team_id" ] || refuse \
    "APNs needs --apns-key-id and --apns-team-id, or those values already in the stack."

  if [ -n "$file" ]; then
    p config set "$NAME:apnsPrivateKey" --secret --raw --non-interactive < "$file" >/dev/null 2>/dev/null ||
      die "Could not encrypt the APNs key in the Pulumi stack. Make its secrets provider available and try again; no key contents were printed."
  fi
  config_set apnsKeyId "$key_id" && config_set apnsTeamId "$team_id" &&
    config_set apnsTopic "$topic" || die "Could not store the public APNs identifiers in the Pulumi stack."
  say "APNs is configured with encrypted $NAME:apnsPrivateKey; the key stays out of Helm values."
}

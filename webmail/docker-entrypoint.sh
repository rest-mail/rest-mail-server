#!/bin/sh
# Replace base path placeholders in the nginx config template.
# NGINX_BASE_PATH must end with / (e.g. /webmail/).
# Falls back to / for root-mounted deployments.

BASE="${NGINX_BASE_PATH:-/}"
# Ensure trailing slash
case "$BASE" in
  */) ;;
  *)  BASE="${BASE}/" ;;
esac
# Derive version without trailing slash for exact-match redirect
BASE_NOSLASH="${BASE%/}"
# Handle root mount: no redirect needed, use a dummy location that never matches
if [ "$BASE_NOSLASH" = "" ]; then
  BASE_NOSLASH="/__never_match_root__"
fi

sed \
  -e "s|__BASE_PATH_NOSLASH__|${BASE_NOSLASH}|g" \
  -e "s|__BASE_PATH__|${BASE}|g" \
  /etc/nginx/conf.d/default.conf.template > /etc/nginx/conf.d/default.conf

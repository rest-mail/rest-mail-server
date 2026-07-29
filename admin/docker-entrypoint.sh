#!/bin/sh
# Replace base path placeholders in the nginx config template.
# NGINX_BASE_PATH must end with / (e.g. /admin/).
# Falls back to / for root-mounted deployments.
#
# Identical to webmail/docker-entrypoint.sh: both frontends serve a SPA under a
# base path that is only known at deploy time, so neither bakes it into the
# image. The duplication is deliberate — each app's docker build context is its
# own directory, so a shared copy would mean building from the repo root.

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

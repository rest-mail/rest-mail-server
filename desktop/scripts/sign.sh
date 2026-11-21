#!/bin/sh
# Signs RESTMail.app for distribution when this Mac holds the Developer ID, and
# ad hoc otherwise, so a build always works and a release is signed where it can be.
#
#   sign.sh <app>            sign (Developer ID with hardened runtime, or ad hoc)
#   sign.sh --notarize <app> notarize and staple an app this script signed with the
#                            Developer ID; warns and continues when Apple refuses
#
# The Developer ID lives in its own keychain, never the login keychain, so codesign
# needs no GUI prompt. Its password is a generic password in the login keychain:
#   service "REST Mail signing keychain", account "restmail-signing".
# Notarization uses the notarytool profile "restmail-notary" stored in that keychain.
set -eu

notarize=false
if [ "${1:-}" = "--notarize" ]; then notarize=true; shift; fi
app=${1:?usage: sign.sh [--notarize] <app>}
keychain=${DESKTOP_SIGN_KEYCHAIN:-$HOME/Library/Keychains/restmail-signing.keychain-db}
identity=${DESKTOP_SIGN_IDENTITY:-Developer ID Application}
profile=${DESKTOP_NOTARY_PROFILE:-restmail-notary}

developer_id=false
if [ -f "$keychain" ] && password=$(security find-generic-password -a restmail-signing -s "REST Mail signing keychain" -w 2>/dev/null); then
  security unlock-keychain -p "$password" "$keychain"
  unset password
  if security find-identity -v -p codesigning "$keychain" | grep -q "\"$identity"; then
    developer_id=true
  fi
fi

if $notarize; then
  $developer_id || { echo "Not notarized: no Developer ID in $keychain." >&2; exit 0; }
elif $developer_id; then
  codesign --force --deep --options runtime --timestamp --keychain "$keychain" --sign "$identity" "$app"
  codesign --verify --deep --strict "$app"
  echo "Signed with $(codesign -dvv "$app" 2>&1 | sed -n 's/^Authority=//p' | head -1), hardened runtime"
else
  codesign --force --deep --sign - "$app"
  codesign --verify --deep --strict "$app"
  echo "Signed ad hoc: no Developer ID in $keychain. This build runs only on this Mac."
  exit 0
fi

$notarize || exit 0
codesign --verify --deep --strict "$app"
archive=$(mktemp -d)/notarize.zip
ditto -c -k --keepParent "$app" "$archive"
if result=$(xcrun notarytool submit "$archive" --keychain-profile "$profile" --keychain "$keychain" --wait 2>&1) \
  && printf '%s\n' "$result" | grep -q 'status: Accepted'; then
  xcrun stapler staple "$app"
  echo "Notarized and stapled."
else
  echo "WARNING: not notarized; other Macs will warn before opening it. Apple said:" >&2
  printf '%s\n' "$result" | grep -iE 'error|status|agreement' | head -5 >&2
fi
rm -rf "$(dirname "$archive")"

# shellcheck shell=bash
# Read one explicitly selected PUBLIC key. This never discovers, generates,
# uploads, or stores a private key, and never promotes a read-only status key.
read_owner_key() {
  local file=${OWNER_KEY:-} key count type fingerprint
  [ -n "$file" ] || return 0
  case "$file" in /*) ;; *) file="${USER_WORKING_DIR:-$PWD}/$file" ;; esac
  [ -f "$file" ] || refuse "No owner public key file at $file. Pass a .pub file; keep its private half on your machine."
  count=$(awk 'NF && $1 !~ /^#/ {n++} END {print n+0}' "$file")
  [ "$count" = 1 ] || refuse "The owner-key file must contain exactly one public SSH key, not a private key or a key list."
  key=$(awk 'NF && $1 !~ /^#/ {print; exit}' "$file")
  read -r type _ <<<"$key"
  case "$type" in
    ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp256|ecdsa-sha2-nistp384|ecdsa-sha2-nistp521) ;;
    *) refuse "Owner keys must be public Ed25519, ECDSA or RSA SSH keys without authorized_keys options. Never provide the private key." ;;
  esac
  need ssh-keygen
  fingerprint=$(printf '%s\n' "$key" | ssh-keygen -l -E sha256 -f - 2>/dev/null) ||
    refuse "That is not a valid public SSH key. The owner-key file must be the public .pub file."
  printf 'Owner recovery key: %s\n' "$fingerprint" >&2
  printf '%s' "$key"
}

owner_key_guidance() {
  if [ -n "$1" ]; then
    say "Owner recovery uses the matching private key on your machine, or one loaded in ssh-agent:" \
      "  restmail reset-admin --server https://$HOST --address admin@$DOMAIN" \
      "Back up that private key securely. The server receives only the public key."
  else
    say "No --owner-key file was selected; existing owner keys in managed values are kept." \
      "Without a registered owner key, password recovery requires cluster administrator access." \
      "For signed-challenge recovery, register your public key with --owner-key /path/to/key.pub" \
      "or Helm api.ownerKeys; keep the private half on your own machine."
  fi
}

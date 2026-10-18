# shellcheck shell=bash
# Relative paths belong to the person running chore, not its task directory.
owner_key_path() {
  local file=$1
  case "$file" in '~/'*) file="$HOME/${file#\~/}" ;; esac
  case "$file" in /*) ;; *) file="${USER_WORKING_DIR:-$PWD}/$file" ;; esac
  printf '%s' "$file"
}

# Select a public key, or explicitly create its private half on this machine.
# stdout carries only the public key; prompts and OpenSSH stay on the terminal.
choose_owner_key() {
  local choice file
  if [ -n "${OWNER_KEY:-}" ]; then read_owner_key; return; fi
  [ -t 0 ] && [ -t 2 ] || return 0
  choice=$(ask_tty $'Owner recovery key:\n  1. Create a new local Ed25519 key\n  2. Use an existing public-key file\n  3. Keep managed owner keys unchanged (or skip)\nChoose [1]: ') || return $?
  case "$choice" in
    '' | 1)
      file=$(ask_tty 'Private-key filename [restmail_owner_ed25519]: ') || return $?
      file=$(owner_key_path "${file:-restmail_owner_ed25519}")
      need python3 ssh-keygen
      python3 "$(dirname "${BASH_SOURCE[0]}")/create-owner-key.py" "$file" </dev/tty >/dev/tty 2>&1 || return $?
      printf 'Saved private owner key: %s\nBack it up securely and keep it out of source control. Only %s.pub is registered.\n' "$file" "$file" >&2
      OWNER_KEY="$file.pub" read_owner_key
      ;;
    2)
      file=$(ask_tty 'Existing public-key filename (.pub): ') || return $?
      [ -n "$file" ] || refuse 'No public-key filename selected.'
      OWNER_KEY="$file" read_owner_key
      ;;
    3) return 0 ;;
    *) refuse 'Choose 1, 2 or 3. Installation stopped without registering a key.' ;;
  esac
}

# Read one explicitly selected PUBLIC key; never promote a status key.
read_owner_key() {
  local file=${OWNER_KEY:-} key count type fingerprint
  [ -n "$file" ] || return 0
  file=$(owner_key_path "$file")
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

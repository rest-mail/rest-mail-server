#!/usr/bin/env bash
# REST Mail core repository scope, copied from the canonical constellation manifest.
# Refresh this list from constellation.json when core membership changes.
# The sourcing CLI consumes this array.
# shellcheck disable=SC2034
CONSTELLATION_REPOSITORIES=(
    "conventions:rest-mail/conventions"
    "deploy:rest-mail/deploy"
    "go-arc:rest-mail/go-arc"
    "go-dkim:rest-mail/go-dkim"
    "go-dmarc:rest-mail/go-dmarc"
    "go-imap:rest-mail/go-imap"
    "go-mtasts:rest-mail/go-mtasts"
    "go-pop3:rest-mail/go-pop3"
    "go-sieve:rest-mail/go-sieve"
    "go-smtp:rest-mail/go-smtp"
    "go-spf:rest-mail/go-spf"
    "homebrew-tap:rest-mail/homebrew-tap"
    "instantmailcheck:rest-mail/instantmailcheck"
    "reference-certgen:rest-mail/reference-certgen"
    "reference-clamav:rest-mail/reference-clamav"
    "reference-dnsmasq:rest-mail/reference-dnsmasq"
    "reference-dovecot:rest-mail/reference-dovecot"
    "reference-fail2ban:rest-mail/reference-fail2ban"
    "reference-mailserver:rest-mail/reference-mailserver"
    "reference-postfix:rest-mail/reference-postfix"
    "reference-rspamd:rest-mail/reference-rspamd"
    "rest-mail-mobile:rest-mail/rest-mail-mobile"
    "rest-mail-server:rest-mail/rest-mail-server"
    "testbed:rest-mail/testbed"
    "website:rest-mail/website"
)

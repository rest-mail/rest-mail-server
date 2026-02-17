#!/bin/bash
set -e

echo "Configuring Dovecot for ${MAIL_DOMAIN}..."

# Default values
: "${MAIL_DOMAIN:=mail1.test}"
: "${DB_HOST:=postgres}"
: "${DB_NAME:=restmail}"
: "${DB_USER:=restmail}"
: "${DB_PASS:=restmail}"
: "${TLS_CERT_PATH:=/certs/${MAIL_DOMAIN}.crt}"
: "${TLS_KEY_PATH:=/certs/${MAIL_DOMAIN}.key}"

# Render dovecot.conf from template
envsubst < /etc/dovecot/templates/dovecot.conf.tmpl > /etc/dovecot/dovecot.conf

# Render SQL config from template
envsubst < /etc/dovecot/templates/dovecot-sql.conf.ext.tmpl > /etc/dovecot/dovecot-sql.conf.ext

# Secure the SQL config (contains database password)
chmod 600 /etc/dovecot/dovecot-sql.conf.ext
chown root:root /etc/dovecot/dovecot-sql.conf.ext

# Ensure mail directory exists
mkdir -p /var/mail/vhosts
chown -R vmail:vmail /var/mail/vhosts

# Trust our CA certificate
if [ -f /certs/ca.crt ]; then
    cp /certs/ca.crt /usr/local/share/ca-certificates/restmail-ca.crt
    update-ca-certificates 2>/dev/null || true
fi

echo "Dovecot configured for ${MAIL_DOMAIN}"

exec "$@"

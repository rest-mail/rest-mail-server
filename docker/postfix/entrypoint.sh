#!/bin/bash
set -e

echo "Configuring Postfix for ${MAIL_DOMAIN}..."

# Default values
: "${MAIL_DOMAIN:=mail1.test}"
: "${MAIL_HOSTNAME:=${MAIL_DOMAIN}}"
: "${DB_HOST:=postgres}"
: "${DB_NAME:=restmail}"
: "${DB_USER:=restmail}"
: "${DB_PASS:=restmail}"
: "${DOVECOT_HOST:=dovecot-${MAIL_DOMAIN%%.*}}"
: "${TLS_CERT_PATH:=/certs/${MAIL_DOMAIN}.crt}"
: "${TLS_KEY_PATH:=/certs/${MAIL_DOMAIN}.key}"

# Render main.cf from template
envsubst < /etc/postfix/templates/main.cf.tmpl > /etc/postfix/main.cf

# Render master.cf from template
cp /etc/postfix/templates/master.cf.tmpl /etc/postfix/master.cf

# Render SQL lookup configs
for tmpl in /etc/postfix/templates/virtual_*.cf.tmpl; do
    filename=$(basename "$tmpl" .tmpl)
    envsubst < "$tmpl" > "/etc/postfix/sql/$filename"
done

# Set permissions on SQL configs (contain passwords)
chmod 640 /etc/postfix/sql/*.cf
chown root:postfix /etc/postfix/sql/*.cf

# Trust our CA certificate
if [ -f /certs/ca.crt ]; then
    cp /certs/ca.crt /usr/local/share/ca-certificates/restmail-ca.crt
    update-ca-certificates 2>/dev/null || true
fi

# Ensure postfix directories exist with correct permissions
mkdir -p /var/spool/postfix/etc
cp /etc/resolv.conf /var/spool/postfix/etc/resolv.conf 2>/dev/null || true
cp /etc/services /var/spool/postfix/etc/services 2>/dev/null || true

echo "Postfix configured for ${MAIL_DOMAIN}"

exec "$@"

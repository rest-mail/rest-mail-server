#!/bin/sh
set -e

# Trust the operator/testbed CA if present: the file TRUSTED_CA_CERT names
# (the chart's trusted-ca mount), else /certs/ca.crt (the testbed's
# reference-certgen), the same default the gateway's config has. The gateway
# itself trusts that CA only for RESTMAIL; in the system roots it also verifies
# an MX's STARTTLS certificate and an MTA-STS policy host (RFC 8461), which use
# the system roots. The gateway runs unprivileged, on a read-only root
# filesystem where it can, so instead of adding the CA to the system store it
# writes the system roots plus the CA to /tmp and points SSL_CERT_FILE there.
# With no writable /tmp it runs on the system roots alone.
ca="${TRUSTED_CA_CERT:-/certs/ca.crt}"
if [ -f "$ca" ] &&
   cat /etc/ssl/certs/ca-certificates.crt "$ca" > /tmp/ca-certificates.crt 2>/dev/null; then
    export SSL_CERT_FILE=/tmp/ca-certificates.crt
fi

exec "$@"

#!/bin/sh
# setup.sh — Create test accounts in the reference mailserver.
#
# Run after the stack is healthy:
#   cd reference && docker compose up -d
#   ./setup.sh

set -e

CONTAINER="ref-mailserver"

echo "Waiting for mailserver to be ready..."
until docker exec "$CONTAINER" ss -lntp 2>/dev/null | grep -q ':25 '; do
    sleep 2
    printf '.'
done
echo ""

echo "Creating alice@ref.test ..."
docker exec "$CONTAINER" setup email add alice@ref.test password

echo "Creating bob@ref.test ..."
docker exec "$CONTAINER" setup email add bob@ref.test password

echo ""
echo "Done. Test accounts:"
echo "  alice@ref.test / password"
echo "  bob@ref.test   / password"
echo ""
echo "Ports (mapped to localhost):"
echo "  SMTP       : 12025"
echo "  IMAP       : 12143  (IMAPS: 12993)"
echo "  Submission : 12587"
echo "  POP3       : 12110  (POP3S: 12995)"

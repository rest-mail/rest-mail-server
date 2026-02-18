#!/bin/sh
set -e

CERT_DIR="/certs"
DOMAINS="mail1.test mail2.test mail3.test"

# Skip if certs already exist
if [ -f "$CERT_DIR/ca.crt" ] && [ -f "$CERT_DIR/mail1.test.crt" ]; then
    echo "Certificates already exist, skipping generation"
    exit 0
fi

echo "Generating self-signed CA and server certificates..."

mkdir -p "$CERT_DIR"

# Generate CA private key
openssl genrsa -out "$CERT_DIR/ca.key" 4096

# Generate CA certificate
openssl req -x509 -new -nodes \
    -key "$CERT_DIR/ca.key" \
    -sha256 -days 3650 \
    -out "$CERT_DIR/ca.crt" \
    -subj "/C=US/ST=Test/L=Test/O=RestMail Test CA/CN=RestMail Test CA"

# Generate certificates for each domain
for DOMAIN in $DOMAINS; do
    echo "Generating certificate for $DOMAIN..."

    # Generate server private key
    openssl genrsa -out "$CERT_DIR/$DOMAIN.key" 2048

    # Generate CSR
    openssl req -new \
        -key "$CERT_DIR/$DOMAIN.key" \
        -out "$CERT_DIR/$DOMAIN.csr" \
        -subj "/C=US/ST=Test/L=Test/O=RestMail/CN=$DOMAIN"

    # Create extensions file for SAN
    cat > "$CERT_DIR/$DOMAIN.ext" <<EOF
authorityKeyIdentifier=keyid,issuer
basicConstraints=CA:FALSE
keyUsage = digitalSignature, nonRepudiation, keyEncipherment, dataEncipherment
subjectAltName = @alt_names

[alt_names]
DNS.1 = $DOMAIN
DNS.2 = *.$DOMAIN
EOF

    # Sign the certificate with our CA
    openssl x509 -req \
        -in "$CERT_DIR/$DOMAIN.csr" \
        -CA "$CERT_DIR/ca.crt" \
        -CAkey "$CERT_DIR/ca.key" \
        -CAcreateserial \
        -out "$CERT_DIR/$DOMAIN.crt" \
        -days 825 \
        -sha256 \
        -extfile "$CERT_DIR/$DOMAIN.ext"

    # Clean up CSR and ext files
    rm -f "$CERT_DIR/$DOMAIN.csr" "$CERT_DIR/$DOMAIN.ext"

    # Set permissions
    chmod 644 "$CERT_DIR/$DOMAIN.crt"
    chmod 600 "$CERT_DIR/$DOMAIN.key"
done

# Clean up CA serial file
rm -f "$CERT_DIR/ca.srl"

echo "Certificate generation complete!"
echo "CA certificate: $CERT_DIR/ca.crt"
for DOMAIN in $DOMAINS; do
    echo "  $DOMAIN: $CERT_DIR/$DOMAIN.crt, $CERT_DIR/$DOMAIN.key"
done

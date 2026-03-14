#!/bin/bash

# Ensure openssl is installed
if ! command -v openssl &> /dev/null; then
    echo "openssl not found. Please install it."
    exit 1
fi

echo "Generating certificates..."

# Generate CA key and certificate
openssl genrsa -out ca.key 4096 2>/dev/null
openssl req -new -x509 -days 365 -key ca.key -out ca.crt \
  -subj "/C=US/ST=State/L=City/O=Organization/OU=Unit/CN=DistributedChatCA" 2>/dev/null

# Generate Server key
openssl genrsa -out server.key 4096 2>/dev/null

# Create SAN config (required by Go 1.15+)
cat > /tmp/san.cnf << EOF
[req]
distinguished_name = req_dn
req_extensions = v3_req
[req_dn]
CN = localhost
[v3_req]
subjectAltName = DNS:localhost,IP:127.0.0.1,IP:0.0.0.0
[v3_ca]
subjectAltName = DNS:localhost,IP:127.0.0.1,IP:0.0.0.0
EOF

# Generate CSR with SANs
openssl req -new -key server.key -out server.csr \
  -subj "/CN=localhost" -config /tmp/san.cnf 2>/dev/null

# Sign the server certificate with SANs
openssl x509 -req -days 365 -in server.csr -CA ca.crt -CAkey ca.key \
  -set_serial 01 -out server.crt \
  -extensions v3_ca -extfile /tmp/san.cnf 2>/dev/null

# Clean up
rm -f server.csr /tmp/san.cnf

echo "✅ Certificates generated: ca.crt, server.crt, server.key"
echo "   SANs: DNS:localhost, IP:127.0.0.1, IP:0.0.0.0"

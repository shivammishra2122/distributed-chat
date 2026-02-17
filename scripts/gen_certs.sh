#!/bin/bash

# Ensure openssl is installed
if ! command -v openssl &> /dev/null; then
    echo "openssl not found. Please install it."
    exit 1
fi

echo "Generating certificates..."

# Generate CA key and certificate
openssl genrsa -out ca.key 4096
openssl req -new -x509 -days 365 -key ca.key -out ca.crt -subj "/C=US/ST=State/L=City/O=Organization/OU=Unit/CN=DistributedChatCA"

# Generate Server key and certificate signing request (CSR)
openssl genrsa -out server.key 4096
openssl req -new -key server.key -out server.csr -subj "/C=US/ST=State/L=City/O=Organization/OU=Unit/CN=localhost"

# Sign the server certificate with the CA
openssl x509 -req -days 365 -in server.csr -CA ca.crt -CAkey ca.key -set_serial 01 -out server.crt

# Clean up CSR
rm server.csr

echo "✅ Certificates generated: ca.crt, server.crt, server.key"

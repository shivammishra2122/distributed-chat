# Build Stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git make

# Copy go mod/sum files first for caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Build the server binary
RUN go build -o /app/server ./cmd/server

# Final Stage (Lightweight)
FROM alpine:latest

# Create data directory
WORKDIR /data

# Copy binary to system bin (separated from data)
COPY --from=builder /app/server /usr/local/bin/server

# Expose ports
EXPOSE 8080 2222

# Volume for snapshots (persistence)
VOLUME /data

# Default Command (since /usr/local/bin is in PATH)
ENTRYPOINT ["server"]
CMD ["-port", "8080", "-ssh", "2222"]

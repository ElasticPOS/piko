ARG GO_VERSION=1
FROM golang:${GO_VERSION}-bookworm as builder

WORKDIR /usr/src/app
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN go build -v -o /app .


FROM debian:bookworm

# Install ca-certificates for TLS connections
RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=builder /app /usr/local/bin/
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Expose ports:
# 8000 - Proxy (HTTP proxy for clients)
# 8001 - Upstream (connections from upstream services)
# 8002 - Admin (metrics, health, status API)
# 8003 - Gossip (inter-node cluster communication)
EXPOSE 8000 8001 8002 8003

# Use entrypoint script to handle IPv6 address formatting for cluster discovery
CMD ["/entrypoint.sh"]

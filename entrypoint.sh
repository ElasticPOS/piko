#!/bin/sh
# Piko entrypoint script for Fly.io
# Resolves cluster members and formats IPv6 addresses correctly

set -e

# Get all IPv6 addresses for the app from internal DNS
# Fly.io returns AAAA records that need brackets when used with ports
CLUSTER_MEMBERS=""

if [ -n "$FLY_APP_NAME" ]; then
    echo "Resolving cluster members from ${FLY_APP_NAME}.internal..."

    # Use getent to resolve all addresses (works with IPv6)
    # Format each IPv6 address with brackets
    for addr in $(getent ahostsv6 "${FLY_APP_NAME}.internal" 2>/dev/null | awk '{print $1}' | sort -u); do
        if [ -n "$addr" ]; then
            formatted="[${addr}]:8003"
            if [ -z "$CLUSTER_MEMBERS" ]; then
                CLUSTER_MEMBERS="$formatted"
            else
                CLUSTER_MEMBERS="${CLUSTER_MEMBERS},${formatted}"
            fi
            echo "  Found node: $formatted"
        fi
    done
fi

if [ -z "$CLUSTER_MEMBERS" ]; then
    echo "No cluster members found, starting as standalone node"
    CLUSTER_ARGS=""
else
    echo "Cluster members: $CLUSTER_MEMBERS"
    CLUSTER_ARGS="--cluster.join=${CLUSTER_MEMBERS}"
fi

# Format local advertise addresses with brackets for IPv6
ADVERTISE_IP="${FLY_PRIVATE_IP}"
if [ -n "$ADVERTISE_IP" ]; then
    # Check if it's an IPv6 address (contains colons)
    case "$ADVERTISE_IP" in
        *:*)
            ADVERTISE_IP="[${ADVERTISE_IP}]"
            ;;
    esac
fi

# JWT auth on the upstream port (8001) so only clients holding a valid license
# token can register/claim endpoints (prevents endpoint hijacking on the
# tunnel). keytana signs licenses with RS256, so verify with keytana's RSA
# PUBLIC key — an HMAC secret cannot verify an asymmetric (RSA) license and
# would reject every token. The desktop sends its license JWT as the upstream
# token; the token's piko.endpoints claim bounds which endpoints it may
# register. Set via: fly secrets set KEYTANA_PUBLIC_KEY="$(cat public.pem)"
# When unset, auth stays disabled so the server still boots.

# Build the argument list with `set --` so the multi-line PEM survives as a
# single argument; the old unquoted "$AUTH_ARGS" word-splitting would mangle it.
set -- server \
    --cluster.node-id-prefix "${FLY_MACHINE_ID:-local}-" \
    --proxy.bind-addr ":8000" \
    --proxy.advertise-addr "${ADVERTISE_IP}:8000" \
    --upstream.bind-addr ":8001" \
    --upstream.advertise-addr "${ADVERTISE_IP}:8001" \
    --admin.bind-addr ":8002" \
    --admin.advertise-addr "${ADVERTISE_IP}:8002" \
    --cluster.gossip.bind-addr ":8003" \
    --cluster.gossip.advertise-addr "${ADVERTISE_IP}:8003" \
    --cluster.abort-if-join-fails=false \
    --log.level "${LOG_LEVEL:-info}"

if [ -n "$CLUSTER_ARGS" ]; then
    set -- "$@" "$CLUSTER_ARGS"
fi

if [ -n "$KEYTANA_PUBLIC_KEY" ]; then
    echo "Upstream JWT auth: ENABLED (RSA)"
    set -- "$@" --upstream.auth.rsa-public-key="${KEYTANA_PUBLIC_KEY}"

    # Where the license carries the permitted endpoints. keytana licenses put a
    # single endpoint id at the root 'endpoint_id' claim; Piko's built-in
    # default is 'piko.endpoints'. Override via UPSTREAM_ENDPOINTS_CLAIM.
    UPSTREAM_ENDPOINTS_CLAIM="${UPSTREAM_ENDPOINTS_CLAIM:-endpoint_id}"
    echo "Upstream endpoints claim: ${UPSTREAM_ENDPOINTS_CLAIM}"
    set -- "$@" --upstream.auth.endpoints-claim="${UPSTREAM_ENDPOINTS_CLAIM}"

    # Require every license to scope itself to specific endpoints via that
    # claim. Without this, a validly-signed token that omits the claim is
    # permitted on EVERY endpoint. Opt out by setting
    # UPSTREAM_REQUIRE_ENDPOINTS=false (e.g. if any license needs full access).
    if [ "${UPSTREAM_REQUIRE_ENDPOINTS:-true}" = "true" ]; then
        echo "Upstream endpoint scoping: REQUIRED"
        set -- "$@" --upstream.auth.require-endpoints
    fi
else
    echo "Upstream JWT auth: DISABLED (KEYTANA_PUBLIC_KEY not set)"
fi

echo "Starting Piko server..."
echo "  Node ID prefix: ${FLY_MACHINE_ID:-local}-"
echo "  Advertise IP: $ADVERTISE_IP"

exec app "$@"
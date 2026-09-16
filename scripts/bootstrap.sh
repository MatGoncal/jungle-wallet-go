#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

COMPOSE=(docker compose -f deploy/docker-compose.yml)
API_URL="${API_URL:-http://localhost:18080}"
KEYCLOAK_TOKEN_URL="${KEYCLOAK_TOKEN_URL:-http://localhost:8081/realms/jungle-wallet/protocol/openid-connect/token}"
KEYCLOAK_INTERNAL_CLIENT_ID="${KEYCLOAK_INTERNAL_CLIENT_ID:-internal-service}"
KEYCLOAK_INTERNAL_CLIENT_SECRET="${KEYCLOAK_INTERNAL_CLIENT_SECRET:-internal-service-secret}"
MIGRATE_DATABASE_URL="${MIGRATE_DATABASE_URL:-postgres://wallet:wallet@localhost:55432/wallet?sslmode=disable}"

log() { printf '[bootstrap] %s\n' "$*"; }

wait_http() {
  local url=$1 name=$2 max=${3:-60}
  local i=0
  until curl -sf "$url" >/dev/null 2>&1; do
    i=$((i + 1))
    if [ "$i" -ge "$max" ]; then
      echo "timeout waiting for $name ($url)" >&2
      return 1
    fi
    sleep 2
  done
}

wait_postgres() {
  local max=${1:-60} i=0
  until docker exec "$("${COMPOSE[@]}" ps -q postgres)" pg_isready -U wallet -d wallet >/dev/null 2>&1; do
    i=$((i + 1))
    if [ "$i" -ge "$max" ]; then
      echo "timeout waiting for postgres" >&2
      return 1
    fi
    sleep 2
  done
}

log "starting dependencies (postgres, keycloak, localstack, migrate, api)..."
"${COMPOSE[@]}" up -d --build

log "waiting for postgres..."
wait_postgres

log "waiting for localstack..."
wait_http "http://localhost:4566/_localstack/health" "localstack" 90

log "waiting for keycloak..."
wait_http "http://localhost:8081/realms/jungle-wallet" "keycloak" 90

log "ensuring API is up after Keycloak is ready..."
"${COMPOSE[@]}" up -d --no-deps api api-1 api-2 api-3

log "running migrations..."
if command -v migrate >/dev/null 2>&1; then
  migrate -path migrations -database "$MIGRATE_DATABASE_URL" up
else
  "${COMPOSE[@]}" run --rm migrate up
fi

log "waiting for API..."
wait_http "${API_URL}/health/live" "api" 90

log "fetching internal-service token..."
TOKEN="$(
  curl -sS -X POST "$KEYCLOAK_TOKEN_URL" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "grant_type=client_credentials" \
    -d "client_id=${KEYCLOAK_INTERNAL_CLIENT_ID}" \
    -d "client_secret=${KEYCLOAK_INTERNAL_CLIENT_SECRET}" \
    | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p'
)"
if [ -z "$TOKEN" ]; then
  echo "failed to obtain access token from Keycloak" >&2
  exit 1
fi

PLAYER_ID="$(uuidgen 2>/dev/null || cat /proc/sys/kernel/random/uuid)"
log "creating wallet (playerId=${PLAYER_ID})..."
HTTP_CODE="$(
  curl -sS -o /tmp/jungle-wallet-bootstrap.json -w '%{http_code}' \
    -X POST "${API_URL}/wallets" \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "Content-Type: application/json" \
    -d "{\"playerId\":\"${PLAYER_ID}\",\"initialBalance\":{\"amount\":\"1000.00\",\"currency\":\"BRL\"}}"
)"

if [ "$HTTP_CODE" -ge 200 ] && [ "$HTTP_CODE" -lt 300 ]; then
  log "wallet created (HTTP ${HTTP_CODE}):"
  cat /tmp/jungle-wallet-bootstrap.json
  printf '\n'
else
  log "POST /wallets returned HTTP ${HTTP_CODE} (endpoint may not be implemented yet):"
  cat /tmp/jungle-wallet-bootstrap.json >&2 || true
  exit 1
fi

log "done."

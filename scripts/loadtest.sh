#!/usr/bin/env bash
# Load test for POST /wagering/transactions (vegeta).
#
# Why vegeta (not hey): each request needs a unique Idempotency-Key and body.
# vegeta reads a targets file with distinct headers/bodies per request; hey
# reuses one body/header set for the whole run.
#
# Env overrides:
#   API_URL          default http://localhost:18080
#   KEYCLOAK_TOKEN_URL
#   WALLETS          number of wallets to open (default 10)
#   DURATION         vegeta -duration (default 10s)
#   RATE             vegeta -rate req/s (default 50)
#   WORKERS          vegeta -workers (default 10)
#   BET_AMOUNT       BET amount per request (default 1.00)
#   INITIAL_BALANCE  wallet opening balance (default 100000.00)
#   PROVIDER_ID      default provider-a
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

API_URL="${API_URL:-http://localhost:18080}"
KEYCLOAK_TOKEN_URL="${KEYCLOAK_TOKEN_URL:-http://localhost:8081/realms/jungle-wallet/protocol/openid-connect/token}"
WALLETS="${WALLETS:-10}"
DURATION="${DURATION:-10s}"
RATE="${RATE:-50}"
WORKERS="${WORKERS:-10}"
BET_AMOUNT="${BET_AMOUNT:-1.00}"
INITIAL_BALANCE="${INITIAL_BALANCE:-100000.00}"
PROVIDER_ID="${PROVIDER_ID:-provider-a}"
PROVIDER_SECRET="${PROVIDER_SECRET:-provider-a-secret}"
INTERNAL_CLIENT_ID="${INTERNAL_CLIENT_ID:-internal-service}"
INTERNAL_CLIENT_SECRET="${INTERNAL_CLIENT_SECRET:-internal-service-secret}"

log() { printf '[loadtest] %s\n' "$*"; }

if ! command -v vegeta >/dev/null 2>&1; then
  echo "vegeta not found. Install with:" >&2
  echo "  go install github.com/tsenart/vegeta/v12@latest" >&2
  exit 1
fi

if ! curl -sf "${API_URL}/health/live" >/dev/null; then
  echo "API not reachable at ${API_URL}/health/live (run make bootstrap / make up first)" >&2
  exit 1
fi

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

fetch_token() {
  local client_id=$1 client_secret=$2
  curl -sS -X POST "$KEYCLOAK_TOKEN_URL" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "grant_type=client_credentials" \
    -d "client_id=${client_id}" \
    -d "client_secret=${client_secret}" \
    | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p'
}

parse_json_field() {
  local json=$1 field=$2
  printf '%s' "$json" | sed -n "s/.*\"${field}\":\"\\([^\"]*\\)\".*/\\1/p" | head -n1
}

duration_to_seconds() {
  local d=$1
  case "$d" in
    *s) printf '%s' "${d%s}" ;;
    *m) printf '%s' "$(( ${d%m} * 60 ))" ;;
    *)  printf '%s' "$d" ;;
  esac
}

snap_metrics() {
  local label=$1 out=$2
  curl -sS "${API_URL}/metrics" >"$out" || true
  log "metrics snapshot (${label}):"
  grep -E '^(wallet_concurrency_conflicts_total|wallet_outbox_lag_seconds|wallet_retries_total) ' "$out" \
    || grep -E '^(wallet_concurrency_conflicts_total|wallet_outbox_lag_seconds|wallet_retries_total)' "$out" \
    || log "(no matching metric lines)"
  grep -E '^wallet_processing_duration_seconds_(sum|count|bucket)' "$out" | head -n 20 || true
}

log "fetching tokens..."
TOKEN_A="$(fetch_token "$PROVIDER_ID" "$PROVIDER_SECRET")"
TOKEN_INT="$(fetch_token "$INTERNAL_CLIENT_ID" "$INTERNAL_CLIENT_SECRET")"
if [ -z "$TOKEN_A" ] || [ -z "$TOKEN_INT" ]; then
  echo "failed to obtain Keycloak tokens" >&2
  exit 1
fi

WALLETS_FILE="$TMPDIR/wallets.tsv"
: >"$WALLETS_FILE"

log "opening ${WALLETS} wallets (initialBalance=${INITIAL_BALANCE})..."
for i in $(seq 1 "$WALLETS"); do
  player_id="$(uuidgen 2>/dev/null || cat /proc/sys/kernel/random/uuid)"
  resp="$(
    curl -sS -X POST "${API_URL}/wallets" \
      -H "Authorization: Bearer ${TOKEN_INT}" \
      -H "Content-Type: application/json" \
      -d "{\"playerId\":\"${player_id}\",\"initialBalance\":{\"amount\":\"${INITIAL_BALANCE}\",\"currency\":\"BRL\"}}"
  )"
  wallet_id="$(parse_json_field "$resp" id)"
  if [ -z "$wallet_id" ]; then
    echo "failed to open wallet #${i}: ${resp}" >&2
    exit 1
  fi
  printf '%s\t%s\n' "$wallet_id" "$player_id" >>"$WALLETS_FILE"
done

DUR_SEC="$(duration_to_seconds "$DURATION")"
# Enough unique targets so vegeta never recycles Idempotency-Key within the run.
TARGET_COUNT=$(( RATE * DUR_SEC + RATE ))
TARGETS="$TMPDIR/targets.txt"
: >"$TARGETS"

log "generating ${TARGET_COUNT} unique vegeta targets (round-robin across ${WALLETS} wallets)..."
mapfile -t WALLET_ROWS <"$WALLETS_FILE"
RUN_ID="$(date +%s%N)"
BODIES="$TMPDIR/bodies"
mkdir -p "$BODIES"

for i in $(seq 0 $((TARGET_COUNT - 1))); do
  row="${WALLET_ROWS[$((i % WALLETS))]}"
  wallet_id="${row%%$'\t'*}"
  player_id="${row#*$'\t'}"
  tx_id="lt-${RUN_ID}-${i}"
  idem_key="${PROVIDER_ID}:${tx_id}"
  body_file="${BODIES}/${i}.json"
  printf '{"providerId":"%s","externalTransactionId":"%s","playerId":"%s","walletId":"%s","roundId":"round-lt-%s","gameId":"fortune-chimp","kind":"BET","money":{"amount":"%s","currency":"BRL"}}' \
    "$PROVIDER_ID" "$tx_id" "$player_id" "$wallet_id" "$i" "$BET_AMOUNT" >"$body_file"

  # vegeta HTTP format: body via @file line (not inline JSON).
  {
    printf 'POST %s/wagering/transactions\n' "$API_URL"
    printf 'Authorization: Bearer %s\n' "$TOKEN_A"
    printf 'Content-Type: application/json\n'
    printf 'Idempotency-Key: %s\n' "$idem_key"
    printf '@%s\n' "$body_file"
    printf '\n'
  } >>"$TARGETS"
done

BEFORE="$TMPDIR/metrics-before.txt"
AFTER="$TMPDIR/metrics-after.txt"
REPORT="$TMPDIR/vegeta-report.txt"
REPORT_JSON="$TMPDIR/vegeta-report.json"

snap_metrics "before" "$BEFORE"

log "running vegeta attack (rate=${RATE}/s duration=${DURATION} workers=${WORKERS})..."
vegeta attack \
  -targets="$TARGETS" \
  -rate="${RATE}" \
  -duration="${DURATION}" \
  -workers="${WORKERS}" \
  -timeout=30s \
  | tee "$TMPDIR/results.bin" \
  | vegeta report | tee "$REPORT"

vegeta report -type=json <"$TMPDIR/results.bin" >"$REPORT_JSON"

snap_metrics "after" "$AFTER"

log "--- metric diff (conflicts / outbox lag / retries) ---"
for metric in wallet_concurrency_conflicts_total wallet_outbox_lag_seconds wallet_retries_total; do
  b="$(grep -E "^${metric}( |$)" "$BEFORE" | awk '{print $NF}' | head -n1)"
  a="$(grep -E "^${metric}( |$)" "$AFTER" | awk '{print $NF}' | head -n1)"
  printf '%s: before=%s after=%s\n' "$metric" "${b:-n/a}" "${a:-n/a}"
done

log "--- vegeta percentiles (from JSON report) ---"
# Prefer python if present; else print raw latencies map keys vegeta emits.
if command -v python3 >/dev/null 2>&1; then
  python3 - "$REPORT_JSON" <<'PY'
import json, sys
r = json.load(open(sys.argv[1]))
lat = r.get("latencies") or {}
# vegeta JSON uses nanoseconds
def ms(ns):
    if ns is None: return "n/a"
    return f"{ns/1e6:.2f}ms"
print(f"requests={r.get('requests')} rate={r.get('rate'):.2f}/s success={r.get('success', 0)*100:.2f}%")
print(f"p50={ms(lat.get('50th'))} p95={ms(lat.get('95th'))} p99={ms(lat.get('99th'))} max={ms(lat.get('max'))}")
errs = r.get("errors") or []
if errs:
    print("errors:")
    for e in errs[:20]:
        print(f"  {e}")
status = r.get("status_codes") or {}
if status:
    print("status_codes:", status)
PY
else
  log "python3 unavailable; see full report above"
  cat "$REPORT"
fi

log "done. targets=${TARGET_COUNT} wallets=${WALLETS} tool=vegeta"
log "note: no RPS floor in the challenge — numbers are for reproducibility, not a pass/fail gate."

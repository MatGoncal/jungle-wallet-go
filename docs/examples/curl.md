# Exemplos curl

Pré-requisito: stack no ar (`make bootstrap` ou `make up` + migrate). API em `http://localhost:18080`.

## Tokens

```sh
# Provedor A (wagering)
export TOKEN_A="$(
  curl -sS -X POST 'http://localhost:8081/realms/jungle-wallet/protocol/openid-connect/token' \
    -H 'Content-Type: application/x-www-form-urlencoded' \
    -d 'grant_type=client_credentials' \
    -d 'client_id=provider-a' \
    -d 'client_secret=provider-a-secret' \
  | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p'
)"

# Serviço interno (abrir carteira / reconciliar)
export TOKEN_INT="$(
  curl -sS -X POST 'http://localhost:8081/realms/jungle-wallet/protocol/openid-connect/token' \
    -H 'Content-Type: application/x-www-form-urlencoded' \
    -d 'grant_type=client_credentials' \
    -d 'client_id=internal-service' \
    -d 'client_secret=internal-service-secret' \
  | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p'
)"
```

Atalho: `make token` → token de `provider-a`.

## Health

```sh
curl -sS http://localhost:18080/health/live
curl -sS http://localhost:18080/health/ready
```

## Abrir carteira

```sh
export PLAYER_ID="$(uuidgen 2>/dev/null || cat /proc/sys/kernel/random/uuid)"

curl -sS -X POST http://localhost:18080/wallets \
  -H "Authorization: Bearer ${TOKEN_INT}" \
  -H 'Content-Type: application/json' \
  -d "{\"playerId\":\"${PLAYER_ID}\",\"initialBalance\":{\"amount\":\"1000.00\",\"currency\":\"BRL\"}}"
```

Guarde `id` da resposta como `WALLET_ID`.

## Aposta (BET)

```sh
curl -sS -X POST http://localhost:18080/wagering/transactions \
  -H "Authorization: Bearer ${TOKEN_A}" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: provider-a:tx-demo-1' \
  -d "{
    \"providerId\": \"provider-a\",
    \"externalTransactionId\": \"tx-demo-1\",
    \"playerId\": \"${PLAYER_ID}\",
    \"walletId\": \"${WALLET_ID}\",
    \"roundId\": \"round-1\",
    \"gameId\": \"fortune-chimp\",
    \"kind\": \"BET\",
    \"money\": {\"amount\": \"25.00\", \"currency\": \"BRL\"}
  }"
```

Replay (mesmo header e corpo): responda com `idempotentReplay: true` e o saldo do processamento original.

## WIN / LOSS / REFUND

```sh
# WIN
curl -sS -X POST http://localhost:18080/wagering/transactions \
  -H "Authorization: Bearer ${TOKEN_A}" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: provider-a:tx-win-1' \
  -d "{
    \"providerId\": \"provider-a\",
    \"externalTransactionId\": \"tx-win-1\",
    \"playerId\": \"${PLAYER_ID}\",
    \"walletId\": \"${WALLET_ID}\",
    \"roundId\": \"round-1\",
    \"gameId\": \"fortune-chimp\",
    \"kind\": \"WIN\",
    \"money\": {\"amount\": \"40.00\", \"currency\": \"BRL\"},
    \"referenceExternalTransactionId\": \"tx-demo-1\"
  }"

# LOSS (amount obrigatoriamente 0.00)
curl -sS -X POST http://localhost:18080/wagering/transactions \
  -H "Authorization: Bearer ${TOKEN_A}" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: provider-a:tx-loss-1' \
  -d "{
    \"providerId\": \"provider-a\",
    \"externalTransactionId\": \"tx-loss-1\",
    \"playerId\": \"${PLAYER_ID}\",
    \"walletId\": \"${WALLET_ID}\",
    \"roundId\": \"round-2\",
    \"gameId\": \"fortune-chimp\",
    \"kind\": \"LOSS\",
    \"money\": {\"amount\": \"0.00\", \"currency\": \"BRL\"}
  }"

# REFUND da BET original
curl -sS -X POST http://localhost:18080/wagering/transactions \
  -H "Authorization: Bearer ${TOKEN_A}" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: provider-a:tx-refund-1' \
  -d "{
    \"providerId\": \"provider-a\",
    \"externalTransactionId\": \"tx-refund-1\",
    \"playerId\": \"${PLAYER_ID}\",
    \"walletId\": \"${WALLET_ID}\",
    \"roundId\": \"round-1\",
    \"gameId\": \"fortune-chimp\",
    \"kind\": \"REFUND\",
    \"money\": {\"amount\": \"25.00\", \"currency\": \"BRL\"},
    \"referenceExternalTransactionId\": \"tx-demo-1\"
  }"
```

## Leituras

```sh
curl -sS "http://localhost:18080/wallets/${WALLET_ID}" \
  -H "Authorization: Bearer ${TOKEN_A}"

curl -sS "http://localhost:18080/wallets/${WALLET_ID}/ledger?limit=50" \
  -H "Authorization: Bearer ${TOKEN_A}"

curl -sS "http://localhost:18080/providers/provider-a/wagering/transactions/tx-demo-1" \
  -H "Authorization: Bearer ${TOKEN_A}"
```

## Reconciliação

```sh
curl -sS -X POST "http://localhost:18080/wallets/${WALLET_ID}/reconciliation" \
  -H "Authorization: Bearer ${TOKEN_INT}"
```

## Publicar operação na fila SQS (LocalStack)

```sh
aws --endpoint-url=http://localhost:4566 sqs send-message \
  --queue-url http://localhost:4566/000000000000/wager-transactions.fifo \
  --message-group-id "${WALLET_ID}" \
  --message-deduplication-id 'provider-a:tx-sqs-1' \
  --message-body "{
    \"messageId\": \"msg-sqs-1\",
    \"type\": \"WagerTransactionRequested\",
    \"occurredAt\": \"2026-09-15T12:00:00.000Z\",
    \"data\": {
      \"providerId\": \"provider-a\",
      \"externalTransactionId\": \"tx-sqs-1\",
      \"idempotencyKey\": \"provider-a:tx-sqs-1\",
      \"playerId\": \"${PLAYER_ID}\",
      \"walletId\": \"${WALLET_ID}\",
      \"roundId\": \"round-sqs\",
      \"gameId\": \"fortune-chimp\",
      \"kind\": \"BET\",
      \"money\": {\"amount\": \"10.00\", \"currency\": \"BRL\"}
    }
  }"
```

Credenciais Locais: `AWS_ACCESS_KEY_ID=test` `AWS_SECRET_ACCESS_KEY=test` `AWS_REGION=us-east-1`.

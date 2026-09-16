# Teste de carga (vegeta)

Sem meta de RPS — o enunciado não define piso. O objetivo é **reprodutibilidade** e números honestos (runner + Prometheus), não um gate pass/fail.

## Ferramenta

**vegeta** (não `hey`): cada request precisa de `Idempotency-Key` e corpo únicos. O vegeta lê um arquivo de targets com header/body distintos por request; o `hey` reutiliza um único body/header no run inteiro.

```sh
go install github.com/tsenart/vegeta/v12@latest
```

## Comando

Pré-requisito: stack no ar (`make bootstrap` ou `make up` + migrate). API em `http://localhost:18080`.

```sh
make loadtest
# ou com overrides:
make loadtest WALLETS=10 DURATION=10s RATE=50 WORKERS=10
```

Variáveis (env / `make VAR=...`):

| Var | Default | Significado |
|-----|---------|-------------|
| `WALLETS` | `10` | Carteiras abertas antes do ataque (round-robin) |
| `DURATION` | `10s` | Duração do ataque vegeta |
| `RATE` | `50` | Requests por segundo |
| `WORKERS` | `10` | Workers vegeta |
| `BET_AMOUNT` | `1.00` | Valor de cada BET |
| `INITIAL_BALANCE` | `100000.00` | Saldo inicial (evita saldo insuficiente) |
| `API_URL` | `http://localhost:18080` | Instância medida em `/metrics` |

Script: [`scripts/loadtest.sh`](../scripts/loadtest.sh).

## Metodologia

1. Token Keycloak `provider-a` (apostas) e `internal-service` (abrir carteiras).
2. Abre `WALLETS` carteiras via `POST /wallets`.
3. Gera `RATE × duração + RATE` targets vegeta com `Idempotency-Key` / `externalTransactionId` únicos e carteiras em round-robin — reforça “carteiras distintas avançam em paralelo”.
4. Snapshot de `GET /metrics` **antes** e **depois**.
5. `vegeta attack` em `POST /wagering/transactions` (kind `BET`).
6. Relatório: throughput, p50/p95/p99, taxa de erro; diff de `wallet_concurrency_conflicts_total`, `wallet_outbox_lag_seconds`, `wallet_retries_total`; buckets de `wallet_processing_duration_seconds`.

Métricas Prometheus são **por processo**. Com várias réplicas no Compose, o script consulta só `API_URL` (porta `18080` por padrão).

## Ambiente da execução documentada

| Item | Valor |
|------|--------|
| Data (UTC) | 2026-09-16T13:19:20Z |
| Host | WSL2 Linux x86_64 (`6.6.87.2-microsoft-standard-WSL2`) |
| CPU | 12 vCPU (`nproc`) |
| Memória | 15 GiB total (~9 GiB available) |
| Docker | 29.7.1 |
| Compose | v5.4.0 |
| vegeta | Runtime go1.24.7 linux/amd64 |
| Stack | `deploy-api` + `api-1`/`api-2`/`api-3`, Postgres, Keycloak, LocalStack |

## Resultado real

Parâmetros: `WALLETS=10` `DURATION=10s` `RATE=50` `WORKERS=10` `BET_AMOUNT=1.00`.

### Runner (vegeta)

| Métrica | Valor |
|---------|--------|
| Requests | 500 |
| Rate / throughput | 50.10 /s · 50.07 /s |
| Success | 100% (`200:500`) |
| p50 | 6.91 ms |
| p95 | 17.64 ms |
| p99 | 34.60 ms |
| max | 109.02 ms |
| mean | 9.02 ms |

### Prometheus (`API_URL` = api na porta 18080)

| Métrica | Antes | Depois |
|---------|-------|--------|
| `wallet_concurrency_conflicts_total` | 0 | 0 |
| `wallet_outbox_lag_seconds` | ~0.056 | ~0.056 |
| `wallet_retries_total` | 0 | 0 |

`wallet_processing_duration_seconds` (channel=`http`, kind=`BET`), delta desta execução ≈ 500 amostras:

- count: 10 → 510
- sum: ~0.075 → ~3.95 s → média de processamento ~7.8 ms (alinhada ao mean do vegeta; o runner inclui RTT HTTP)

Distribuição aproximada nos buckets padrão (após o run, acumulado no processo): maioria abaixo de 10 ms (`le="0.01"`), quase tudo abaixo de 25 ms (`le="0.025"`).

## Limitações

- Números locais (WSL2 + Compose); não são baseline de produção.
- Uma única porta de métricas; outras réplicas não entram no snapshot.
- Só BET síncrono HTTP — não mede consumer SQS sob a mesma carga.

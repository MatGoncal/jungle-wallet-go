# jungle-wallet-go

Serviço de carteira e apostas em Go (desafio Jungle Gaming): API HTTP + consumidor SQS FIFO, com PostgreSQL, Keycloak (OIDC) e LocalStack.

Requisitos: [`docs/CHALLENGE.md`](docs/CHALLENGE.md). 
Decisões: [`ARCHITECTURE.md`](ARCHITECTURE.md). 
Rastreio: [`docs/REQUIREMENTS-TRACE.md`](docs/REQUIREMENTS-TRACE.md). 
Agentes: [`AGENTS.md`](AGENTS.md). 
Exemplos: [`docs/examples/curl.md`](docs/examples/curl.md). 
Collection Postman: [`docs/examples/jungle-wallet.postman_collection.json`](docs/examples/jungle-wallet.postman_collection.json). 
Diagramas: [`docs/diagrams/`](docs/diagrams/).

## Pré-requisitos

- Docker e Docker Compose
- Go **1.24.7** (mesmo valor em `go.mod` e `Dockerfile`)
- `curl`
- Opcional no host: [`golang-migrate`](https://github.com/golang-migrate/migrate), `golangci-lint`

## Clone limpo → primeira chamada autenticada

```sh
git clone <url-do-repo> jungle-wallet-go
cd jungle-wallet-go
cp .env.example .env   # opcional; útil se rodar a API fora do Compose
make bootstrap
```

`make bootstrap` sobe o Compose (`--build`), espera Postgres / LocalStack / Keycloak, aplica migrations, obtém token do client `internal-service` e cria uma carteira via `POST /wallets`.

Portas no host:

| Serviço | Porta |
| --- | --- |
| API | `18080` (instâncias extras: `18081`–`18083`) |
| Postgres | `55432` |
| Keycloak | `8081` |
| LocalStack | `4566` |

Alternativa manual:

```sh
make up
make migrate-up
make token          # access_token do client provider-a
```

Derrubar (volumes inclusive): `make down`.

## Variáveis de ambiente

Valores locais em [`.env.example`](.env.example) — sem segredos reais de produção. Principais:

| Variável | Uso |
| --- | --- |
| `HTTP_ADDR` | Bind da API (container `:8080`) |
| `API_URL` | URL no host para scripts (`http://localhost:18080`) |
| `DATABASE_URL` / `MIGRATE_DATABASE_URL` | Postgres |
| `SQS_*` / `AWS_*` | LocalStack e filas FIFO |
| `OIDC_ISSUER_URL` / `OIDC_DISCOVERY_URL` | Validação JWKS (issuer público vs discovery interno no Compose) |
| `KEYCLOAK_*` | Endpoint e clients para `make token` / bootstrap |
| `SHUTDOWN_TIMEOUT` | Prazo de drain no `SIGTERM` |

No Compose a API já recebe essas variáveis; o `.env` serve sobretudo para API e scripts no host.

## Filas SQS

Provisionadas automaticamente no start do LocalStack por [`deploy/localstack/init-sqs.sh`](deploy/localstack/init-sqs.sh):

| Fila | Papel |
| --- | --- |
| `wager-transactions.fifo` | Entrada de operações (visibility 30s, `maxReceiveCount` 5 → DLQ) |
| `wager-transactions-dlq.fifo` | Dead-letter |
| `wallet-events.fifo` | Eventos publicados pelo worker de outbox |

IAM de exemplo: usuário consumidor (Receive/Delete na fila de entrada) e publisher (Send na fila de eventos).

Roteamento documentado: `MessageGroupId` = `walletId`; `MessageDeduplicationId` = chave de idempotência (entrada) ou `eventId` (saída). Detalhes em `ARCHITECTURE.md`.

## Migrations

Arquivos em `migrations/` (`up` / `down`), aplicados pelo serviço `migrate` do Compose ou pela CLI:

```sh
make migrate-up      # aplica todas
make migrate-down    # reverte 1 versão
```

## Executar a aplicação

Com dependências já no Compose:

```sh
make up              # inclui build da API e migrate
# ou, API no host (após up de postgres/keycloak/localstack + migrate):
go run ./cmd/api
```

Health:

```sh
curl -sS http://localhost:18080/health/live
curl -sS http://localhost:18080/health/ready
```

Métricas Prometheus: `GET /metrics`.

## Autenticação (Keycloak)

Realm `jungle-wallet` importado na subida. Clients `client_credentials`:

| Client | Segredo (local) | Papel |
| --- | --- | --- |
| `provider-a` / `provider-b` | `provider-a-secret` / `provider-b-secret` | claim `provider_id` + roles `wagering:write` / `wagering:read` |
| `internal-service` | `internal-service-secret` | role `wallet:admin` (`POST /wallets`, reconciliação) |

Token do provedor:

```sh
TOKEN="$(make token)"
```

Token interno (bootstrap / carteira):

```sh
curl -sS -X POST "http://localhost:8081/realms/jungle-wallet/protocol/openid-connect/token" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "grant_type=client_credentials" \
  -d "client_id=internal-service" \
  -d "client_secret=internal-service-secret"
```

Fluxos autenticados completos: [`docs/examples/curl.md`](docs/examples/curl.md).

## Collection Postman (fumaça)

Importe no Postman:

- Collection: [`docs/examples/jungle-wallet.postman_collection.json`](docs/examples/jungle-wallet.postman_collection.json)
- Environment: [`docs/examples/jungle-wallet.local.postman_environment.json`](docs/examples/jungle-wallet.local.postman_environment.json)

Rode de cima para baixo no Collection Runner (stack no ar). Cada request guarda variáveis (`accessToken`, `internalToken`, `walletId`, …) e asserta status/campos.

Via CLI ([newman](https://www.npmjs.com/package/newman)):

```sh
newman run docs/examples/jungle-wallet.postman_collection.json \
  -e docs/examples/jungle-wallet.local.postman_environment.json
```

## Testes

| Comando | Escopo |
| --- | --- |
| `make test` / `go test ./...` | Unitários (pacotes que compilam sem tags) |
| `make test-race` / `go test -race ./...` | Unitários com detector de corrida |
| `make vet` / `go vet ./...` | Análise estática |
| `make lint` | `golangci-lint` |
| `make ci` | `gofmt -l`, vet, lint, `test-race` |
| `make test-integration` | `-tags=integration` em `test/integration/...` (Postgres, Keycloak, LocalStack reais) |
| `make test-cluster` | `-tags=cluster` em `test/cluster/...` (3 processos da API) |
| `make test-all` | race + integration + cluster |
| `make cover` | cobertura unitária |

Integração / cluster precisam do stack (Compose ou testcontainers). Atalho com Compose já no ar:

```sh
USE_COMPOSE=1 make test-integration
USE_COMPOSE=1 make test-cluster
```

CI: [`.github/workflows/ci.yml`](.github/workflows/ci.yml).

## Validação rápida (checkout limpo)

Equivalentes ao enunciado §15:

```sh
docker compose -f deploy/docker-compose.yml up -d --build
go vet ./...
go test ./...
go test -race ./...
```

Ou `make bootstrap` + `make ci` (+ integration/cluster conforme acima).

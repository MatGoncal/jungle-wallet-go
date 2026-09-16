# jungle-wallet-go — guia para agentes

Requisitos funcionais e critérios eliminatórios: [`docs/CHALLENGE.md`](docs/CHALLENGE.md). Rastreio por seção: [`docs/REQUIREMENTS-TRACE.md`](docs/REQUIREMENTS-TRACE.md).

## Execução local

Pré-requisitos: Docker, Go 1.24.x (ou `~/sdk/go/bin/go`), `golangci-lint`, `curl`. Opcional: CLI [`migrate`](https://github.com/golang-migrate/migrate) no PATH.

| Comando | Efeito |
| --- | --- |
| `make bootstrap` | Compose, health, migrate, token interno, `POST /wallets` de fumaça |
| `make up` / `make down` | Sobe ou derruba o stack (`deploy/docker-compose.yml`) |
| `make migrate-up` / `make migrate-down` | Migrations (CLI em localhost ou serviço `migrate` no Compose) |
| `make token` | Token `client_credentials` do client `provider-a` (Keycloak) |
| `make fmt` / `make vet` / `make lint` | Formatação, análise estática |
| `make test` / `make test-race` | Unitários (race nos pacotes que compilam) |
| `make test-integration` | `-tags=integration` em `test/integration/...` |
| `make test-cluster` | `-tags=cluster` em `test/cluster/...` |
| `make test-all` | Race + integration + cluster |
| `make cover` | Cobertura unitária |
| `make ci` | Gate local: fmt, vet, lint, `test-race` |

Copie `.env.example` para `.env` quando rodar a API fora do Compose.

## Mapa de pacotes

| Caminho | Responsabilidade |
| --- | --- |
| `cmd/api` | `main`, módulo Fx, wiring de transporte e infra |
| `internal/domain/*` | Money, wallet, wagering, eventos, erros — sem I/O |
| `internal/app` | Casos de uso, ports (interfaces), orquestração de domínio |
| `internal/infra/*` | Config, Postgres/pgx, SQS, auth OIDC, observabilidade |
| `internal/transport/*` | HTTP, workers (consumidor, outbox, referências) |
| `migrations/` | SQL versionado (`up`/`down`) |
| `deploy/` | Compose, Keycloak realm, init LocalStack |
| `test/integration/` | Testes com Postgres/IdP/SQS reais (`integration` tag) |
| `test/cluster/` | Múltiplos processos / concorrência pesada (`cluster` tag) |
| `docs/` | Desafio, arquitetura, rastreio de requisitos |

Fluxo esperado: transporte → app → domínio; persistência e mensageria implementam ports em `internal/app`.

## Regras inegociáveis

1. **Dinheiro** — Nunca `float32`/`float64` no caminho monetário (parse, cálculo, JSON de negócio, SQL). Domínio e `internal/infra/postgres` são vigiados pelo linter.
2. **Outbox** — Eventos externos só saem pelo worker de outbox, depois do commit que gravou o registro. Nada de publish SQS dentro do caso de uso ou do handler HTTP no mesmo fluxo da transação.
3. **Repositório** — Repositório Postgres não abre transação; recebe `pgx.Tx` (ou equivalente) do unit of work / caso de uso.
4. **Lock** — Coordenação por carteira; lock global ou fora do escopo da wallet é proibido.
5. **Domínio puro** — `internal/domain` não importa Fx, HTTP, AWS SDK, SQS, pgx, zap, etc.

Idempotência persistente, ledger append-only, inbox/outbox na mesma transação SQL que saldo e ledger quando aplicável — detalhes em `CHALLENGE.md`.

## Antes de considerar pronto

1. `make fmt` (ou `make ci`)
2. `make vet`
3. `make lint`
4. `make test-race`
5. Se tocou infra, SQL, auth, filas ou compose: `make test-integration` (e cluster se alterou concorrência multi-processo)

Documente decisões novas em [`ARCHITECTURE.md`](ARCHITECTURE.md) quando o desafio pedir justificativa. Exemplos curl: [`docs/examples/curl.md`](docs/examples/curl.md). Diagramas: [`docs/diagrams/`](docs/diagrams/).

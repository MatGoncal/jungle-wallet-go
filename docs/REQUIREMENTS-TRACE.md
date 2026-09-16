# Matriz de rastreabilidade

Fonte: [`CHALLENGE.md`](CHALLENGE.md). Cada exigência numerada aponta para artefato e teste que a exercita.

## Por seção do enunciado

| ID | Exigência | Código / artefato | Teste |
| --- | --- | --- | --- |
| §1.1 | API HTTP + consumidor com mesmas garantias | `cmd/api`, `internal/transport/http`, `internal/transport/worker/consumer.go` | `TestCluster_HTTPAndSQSSameOperation` |
| §1.2 | Correto com várias instâncias e falhas entre etapas | `deploy/docker-compose.yml` (`api-1..3`), outbox/inbox | `test/cluster/scenarios_test.go` (cenários 1–8) |
| §2.1 | IdP OAuth2/OIDC externo (Keycloak) | `deploy/keycloak/jungle-wallet-realm.json`, `internal/infra/auth` | `TestAuthz_NoFinancialEffect`, `auth_test.go` |
| §2.2 | `providerId` pelo token; isolamento entre provedores | `auth.RequireProviderMatch`, handlers | `TestAuthz_NoFinancialEffect` |
| §2.3 | Operações de carteira só serviço interno | roles `wallet:admin` nas rotas | `TestAuthz_NoFinancialEffect` |
| §2.4 | Credenciais/políticas no broker + validação no consumidor | `deploy/localstack/init-sqs.sh`, consumer | `TestSQS_PoisonGoesTowardDLQ`, messaging |
| §3.1 | At-least-once: HTTP e SQS repetidos | idempotência + inbox | `TestProcessWagerBetAndReplaySnapshot`, `TestInbox_SameMessageIDTwice`, cluster 50× |
| §3.2 | Reversão antes da referência | `PENDING_REFERENCE` + worker | `TestPendingReference_ResolvedAndExpired`, `TestCluster_RefundBeforeReference_ThenExpire` |
| §3.3 | Simultâneo na mesma carteira | `FOR UPDATE` + version | `TestProcessWagerConcurrentInsufficientFunds`, `TestCluster_TwoBets80On100` |
| §3.4 | Crash antes/depois do commit | outbox/inbox, Delete pós-commit | `TestCluster_KillBetweenCommitAndDelete_Replay`, `TestCluster_RestartPreservesConsistency` |
| §3.5 | Publicação repetida de evento | outbox `eventId` estável | `TestOutbox_EventIDPreservedOnRepublish` |
| §3.6 | Indisponibilidade transitória PG/SQS | `apperr.ErrTransient` → 503 | mapeamento em `handlers.go` + ready check |
| §4.1 | Go + versão em `go.mod` e Dockerfile | `go.mod` `1.24.7`, `Dockerfile` | build Compose |
| §4.2 | Modules versionados | `go.mod` / `go.sum` | CI |
| §4.3 | Uber Fx | `internal/appfx`, `cmd/api` | `TestFx_LifecycleStartsAndStops` |
| §4.4 | HTTP `net/http` | `internal/transport/http` | handlers + authz |
| §4.5 | PostgreSQL + migrations up/down | `migrations/`, `make migrate-*` | `TestMigrations_UpDownUp` |
| §4.6 | SQS via LocalStack | `deploy/localstack/init-sqs.sh` | messaging + cluster |
| §4.7 | Docker Compose | `deploy/docker-compose.yml` | `make bootstrap` |
| §4.8 | `go test` + `-race` | Makefile | `make test-race`, CI |
| §4.9 | pgx SQL explícito; tx entre repositórios | `internal/infra/postgres` | `TestRepositories_UnitOfWorkCommitAndRollback` |
| §4.10 | Domínio independente de Fx/HTTP/SQS/pgx | `internal/domain/*` imports | `go test ./internal/domain/...` |
| §4.11 | Fx Lifecycle (init, cancel, shutdown, close order) | workers + pool hooks | `TestFx_LifecycleStartsAndStops` |
| §5.1 | Sem float no dinheiro | `money.Parse`, forbidigo | `money_test.go` |
| §5.2 | Idempotência persistente | tabelas + hash | `TestProcessWager_ReplayReturnsOriginalBalance`, cluster restart |
| §5.3 | Invariantes no banco | constraints/trigger | `constraints_test.go` |
| §5.4 | Evento só pós-commit (outbox) | `worker/outbox.go` | `TestOutbox_TwoPublishersCompete` |
| §5.5 | Ledger append-only | trigger + GRANT | `TestConstraints_LedgerMathAndAppendOnly` |
| §5.6 | Sem lock global; paralelo entre carteiras | lock por wallet | `TestCluster_DistinctWalletsParallel` |
| §5.7 | Impedir lost update | `FOR UPDATE` + version | cluster + process_wager concurrent |
| §5.8 | Unicidade / não negatividade / imutabilidade no schema | `000001_init.up.sql` | constraints_test |
| §6.0 | Encapsulamento, criação≠reidratação, errors.Is/As, context | domain packages | domain `*_test.go`, `apperr_test.go` |
| §6.1 | Money int64, parse, overflow, escala 2, ISO | `internal/domain/money` | `money_test.go` |
| §6.2 | Wallet, versão, débito≥0, moeda | `internal/domain/wallet` | `wallet_test.go` |
| §6.3 | WagerTransaction estados e OPENING interno | `internal/domain/wagering` | `transaction_test.go` |
| §6.4 | LedgerEntry valida balanceAfter; unique wallet+tx | domain + migration | domain + `TestConstraints_LedgerMathAndAppendOnly` |
| §6.5 | Inbox/outbox na mesma tx do domínio | consumer + repos | messaging + `TestRepositories_UnitOfWorkCommitAndRollback` |
| §7.1 | BET/WIN/LOSS/REFUND/ROLLBACK regras | `ProcessWagerTransaction` | `process_wager_test.go`, usecase_test |
| §7.2 | Resolução de referência e mismatches | `processReversal` | pending ref tests |
| §7.3 | Uma reversão bem-sucedida por aposta (REFUND∪ROLLBACK) | índice parcial + código | `TestConstraints_OneReversalPerReference` |
| §7.4 | `REVERSAL_INSUFFICIENT_FUNDS` ≠ `INSUFFICIENT_FUNDS` | `apperr` + process | domain/app |
| §7.5 | PENDING_REFERENCE + worker TTL | `resolve_pending.go`, `worker/reference.go` | `TestPendingReference_*`, cluster refund |
| §7.6 | failureCode estável | `apperr.Code*` + ARCHITECTURE | respostas 422 |
| §8.1 | Coordenação por carteira, ≥3 processos | compose api-1..3 | `TestCluster_ThreeInstancesSimultaneously` |
| §8.2 | 100 BRL + 2×80 BET → 20 + 1 débito | ProcessWager + cluster | `TestCluster_TwoBets80On100` |
| §9.1 | POST /wallets + OPENING/ledger/outbox ou zero | `OpenWallet`, handlers | `TestOpenWallet_*` |
| §9.2 | GETs wallet/ledger/tx + cursor estável | handlers, `cursor` | `TestLedger_PaginationNoGapOverlap`, `cursor_test` |
| §9.3 | POST wagering + Idempotency-Key obrigatório | handlers | authz + process tests |
| §9.4 | Hash canônico HTTP≡SQS; replay saldo original; conflitos | `wagering.CanonicalHash`, ProcessWager | `TestProcessWager_*`, usecase_test |
| §9.5 | Reconciliação REPEATABLE READ, difference, métrica | `queries` reconcile | `TestReconciliation_RepeatableReadConsistent` |
| §9.6 | /health/live e /ready | `observability/health.go` | bootstrap / Fx |
| §10.1 | Filas FIFO + DLQ redrive | `init-sqs.sh` | messaging poison |
| §10.2 | Caso de uso compartilhado; Delete pós-commit | consumer | cluster kill + HTTP/SQS |
| §10.3 | Visibility 30s, maxReceiveCount 5, malformada→DLQ | init + consumer | `TestSQS_PoisonGoesTowardDLQ` |
| §10.4 | SIGTERM: para poll, drena ou libera visibility | consumer lifecycle | Fx lifecycle |
| §10.5 | MessageGroupId=walletId; Dedup=idempotency key | ARCHITECTURE + publish path | documentado + cluster |
| §11.1 | Outbox concorrente SKIP LOCKED | `worker/outbox.go` | `TestOutbox_TwoPublishersCompete` |
| §11.2 | Eventos tipados exigidos | `internal/domain/event` | `event_test.go` |
| §12.1 | Logs JSON com ids de rastreio | `observability/context.go` | `context_test.go` |
| §12.2 | Métricas Prometheus listadas | `observability/metrics.go` | `/metrics` + reconcile path |
| §13.1 | Unitários Money/Wallet/estados/tipos/hash | `internal/domain`, `internal/app` | `make test-race` |
| §13.2 | Integração real PG/IdP/LocalStack | `test/integration` | `make test-integration` |
| §13.3 | Authz sem efeito financeiro | `authz_fx_test.go` | `TestAuthz_NoFinancialEffect` |
| §13.4 | Oito cenários + HTTP×SQS + saldo=ledger | `test/cluster` | todos `TestCluster_*` |
| §14 | Critérios 100 pts / eliminatórios | esta matriz + ARCHITECTURE | checklist abaixo |
| §15.1 | README, .env.example, Compose, migrations | raiz do repo | revisão manual + `make bootstrap` |
| §15.2 | ARCHITECTURE decisões + limitações | `ARCHITECTURE.md` | — |
| §15.3 | Comandos compose / test / race / vet | README, Makefile | `make ci` |
| §15.4 | Build tags integration/cluster | Makefile | `test-integration`, `test-cluster` |
| §15.5 | gofmt + deps reproduzíveis | `make ci`, `go.sum` | CI `gofmt -l` |

## Eliminatórios

| Item | Status | Evidência |
| --- | --- | --- |
| Auth efetiva nos endpoints de negócio | ok | Middleware JWKS + roles; `TestAuthz_NoFinancialEffect` |
| Sem acesso não autorizado a transações | ok | isolamento provider-a/b; 403/404 |
| Sem float no dinheiro | ok | `Money` + forbidigo domain/postgres |
| Sem saldo negativo por concorrência | ok | CHECK + FOR UPDATE + `TestCluster_TwoBets80On100` |
| Sem movimentação duplicada | ok | uniques + idempotência + cluster 50× |
| Idempotência não só em memória | ok | Postgres + restart cluster |
| Não depende de uma única instância | ok | api-1..3 + testes cluster |
| Publish só pós-commit | ok | outbox worker separado |
| Ledger auditável | ok | trigger + grants + constraints_test |
| Integration sem mock total PG/SQS/IdP | ok | testcontainers / Compose reais |

## Validação a partir de clone limpo

```sh
cp .env.example .env
make bootstrap          # compose --build, migrate, token, POST /wallets
make ci                 # gofmt, vet, lint, test -race
USE_COMPOSE=1 make test-integration
USE_COMPOSE=1 make test-cluster
```

Equivalentes §15: `docker compose -f deploy/docker-compose.yml up --build`, `go vet ./...`, `go test ./...`, `go test -race ./...`.

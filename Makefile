COMPOSE := docker compose -f deploy/docker-compose.yml
MIGRATE_DATABASE_URL ?= postgres://wallet:wallet@localhost:55432/wallet?sslmode=disable
MIGRATIONS_PATH ?= migrations
KEYCLOAK_TOKEN_URL ?= http://localhost:8081/realms/jungle-wallet/protocol/openid-connect/token
API_URL ?= http://localhost:18080

GO ?= go
ifeq (, $(shell command -v go 2>/dev/null))
GO := /home/matheus_goncalves/sdk/go/bin/go
endif

GOLANGCI_LINT ?= golangci-lint
ifeq (, $(shell command -v $(GOLANGCI_LINT) 2>/dev/null))
GOLANGCI_LINT := $(HOME)/go/bin/golangci-lint
endif

.PHONY: up down migrate-up migrate-down token fmt vet lint test test-race test-integration test-cluster test-all cover ci bootstrap

up:
	$(COMPOSE) up -d --build

down:
	$(COMPOSE) down -v

migrate-up:
	@if command -v migrate >/dev/null 2>&1; then \
		migrate -path $(MIGRATIONS_PATH) -database "$(MIGRATE_DATABASE_URL)" up; \
	else \
		$(COMPOSE) run --rm migrate up; \
	fi

migrate-down:
	@if command -v migrate >/dev/null 2>&1; then \
		migrate -path $(MIGRATIONS_PATH) -database "$(MIGRATE_DATABASE_URL)" down 1; \
	else \
		$(COMPOSE) run --rm migrate down 1; \
	fi

token:
	@curl -sS -X POST "$(KEYCLOAK_TOKEN_URL)" \
		-H "Content-Type: application/x-www-form-urlencoded" \
		-d "grant_type=client_credentials" \
		-d "client_id=provider-a" \
		-d "client_secret=provider-a-secret" | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p'

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

lint:
	$(GOLANGCI_LINT) run ./...

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

test-integration:
	$(GO) test -race -tags=integration ./test/integration/...

test-cluster:
	$(GO) test -race -tags=cluster ./test/cluster/...

test-all: test-race test-integration test-cluster

cover:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out

ci:
	@test -z "$$(gofmt -l .)" || (gofmt -l .; exit 1)
	$(GO) vet ./...
	$(GOLANGCI_LINT) run ./...
	$(GO) test -race ./...

bootstrap:
	bash scripts/bootstrap.sh

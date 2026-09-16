CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE wallets (
    id              UUID PRIMARY KEY,
    player_id       UUID NOT NULL,
    currency        CHAR(3) NOT NULL,
    balance_minor   BIGINT NOT NULL,
    version         BIGINT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL,
    CONSTRAINT wallets_balance_non_negative CHECK (balance_minor >= 0),
    CONSTRAINT wallets_version_positive CHECK (version >= 1),
    CONSTRAINT wallets_player_currency_unique UNIQUE (player_id, currency)
);

CREATE TABLE wager_transactions (
    id                                  UUID PRIMARY KEY,
    origin                              TEXT NOT NULL,
    provider_id                         TEXT,
    external_transaction_id             TEXT,
    idempotency_key                     TEXT,
    payload_hash                        TEXT,
    wallet_id                           UUID NOT NULL REFERENCES wallets(id),
    player_id                           UUID NOT NULL,
    round_id                            TEXT,
    game_id                             TEXT,
    kind                                TEXT NOT NULL,
    amount_minor                        BIGINT NOT NULL,
    currency                            CHAR(3) NOT NULL,
    reference_external_transaction_id   TEXT,
    reference_transaction_id            UUID REFERENCES wager_transactions(id),
    status                              TEXT NOT NULL,
    failure_code                        TEXT,
    result_balance_minor                BIGINT,
    created_at                          TIMESTAMPTZ NOT NULL,
    updated_at                          TIMESTAMPTZ NOT NULL,
    CONSTRAINT wager_kind_check CHECK (kind IN ('OPENING','BET','WIN','LOSS','REFUND','ROLLBACK')),
    CONSTRAINT wager_status_check CHECK (status IN ('PENDING','PENDING_REFERENCE','PROCESSED','REJECTED','FAILED')),
    CONSTRAINT wager_origin_check CHECK (origin IN ('INTERNAL','EXTERNAL')),
    CONSTRAINT wager_opening_shape CHECK (
        (origin = 'INTERNAL' AND kind = 'OPENING'
            AND provider_id IS NULL
            AND external_transaction_id IS NULL
            AND idempotency_key IS NULL
            AND payload_hash IS NULL
            AND round_id IS NULL
            AND game_id IS NULL
            AND reference_external_transaction_id IS NULL
            AND amount_minor > 0)
        OR
        (origin = 'EXTERNAL' AND kind <> 'OPENING'
            AND provider_id IS NOT NULL
            AND external_transaction_id IS NOT NULL
            AND idempotency_key IS NOT NULL
            AND payload_hash IS NOT NULL
            AND round_id IS NOT NULL
            AND game_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX wager_provider_external_uidx
    ON wager_transactions (provider_id, external_transaction_id)
    WHERE origin = 'EXTERNAL';

CREATE UNIQUE INDEX wager_provider_idempotency_uidx
    ON wager_transactions (provider_id, idempotency_key)
    WHERE origin = 'EXTERNAL';

CREATE UNIQUE INDEX wager_one_opening_per_wallet_uidx
    ON wager_transactions (wallet_id)
    WHERE kind = 'OPENING';

CREATE UNIQUE INDEX wager_one_reversal_per_reference_uidx
    ON wager_transactions (reference_transaction_id)
    WHERE status = 'PROCESSED'
      AND kind IN ('REFUND', 'ROLLBACK')
      AND reference_transaction_id IS NOT NULL;

CREATE TABLE wallet_ledger_entries (
    id                UUID PRIMARY KEY,
    wallet_id         UUID NOT NULL REFERENCES wallets(id),
    transaction_id    UUID NOT NULL REFERENCES wager_transactions(id),
    direction         TEXT NOT NULL,
    amount_minor      BIGINT NOT NULL,
    currency          CHAR(3) NOT NULL,
    balance_before    BIGINT NOT NULL,
    balance_after     BIGINT NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL,
    CONSTRAINT ledger_direction_check CHECK (direction IN ('DEBIT','CREDIT')),
    CONSTRAINT ledger_amount_positive CHECK (amount_minor > 0),
    CONSTRAINT ledger_balance_math CHECK (
        (direction = 'CREDIT' AND balance_after = balance_before + amount_minor)
        OR
        (direction = 'DEBIT' AND balance_after = balance_before - amount_minor)
    ),
    CONSTRAINT ledger_balance_non_negative CHECK (balance_after >= 0 AND balance_before >= 0),
    CONSTRAINT ledger_wallet_tx_unique UNIQUE (wallet_id, transaction_id)
);

CREATE INDEX ledger_wallet_created_id_idx
    ON wallet_ledger_entries (wallet_id, created_at, id);

CREATE OR REPLACE FUNCTION forbid_ledger_mutation()
RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'wallet_ledger_entries is append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER wallet_ledger_append_only
    BEFORE UPDATE OR DELETE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE PROCEDURE forbid_ledger_mutation();

CREATE TABLE inbox_messages (
    id              UUID PRIMARY KEY,
    consumer_name   TEXT NOT NULL,
    message_id      TEXT NOT NULL,
    payload_hash    TEXT NOT NULL,
    received_at     TIMESTAMPTZ NOT NULL,
    completed_at    TIMESTAMPTZ,
    CONSTRAINT inbox_consumer_message_unique UNIQUE (consumer_name, message_id)
);

CREATE TABLE outbox_events (
    id                UUID PRIMARY KEY,
    aggregate_id      UUID NOT NULL,
    event_type        TEXT NOT NULL,
    payload           JSONB NOT NULL,
    correlation_id    TEXT,
    causation_id      UUID,
    occurred_at       TIMESTAMPTZ NOT NULL,
    attempts          INT NOT NULL DEFAULT 0,
    next_attempt_at   TIMESTAMPTZ NOT NULL,
    published_at      TIMESTAMPTZ,
    locked_until      TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL
);

CREATE INDEX outbox_unpublished_idx
    ON outbox_events (next_attempt_at, id)
    WHERE published_at IS NULL;

CREATE TABLE reference_retry_state (
    transaction_id    UUID PRIMARY KEY REFERENCES wager_transactions(id),
    attempts          INT NOT NULL DEFAULT 0,
    next_attempt_at   TIMESTAMPTZ NOT NULL,
    expires_at        TIMESTAMPTZ NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL
);

DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'wallet_app') THEN
        CREATE ROLE wallet_app LOGIN PASSWORD 'wallet_app';
    END IF;
END$$;

GRANT CONNECT ON DATABASE wallet TO wallet_app;
GRANT USAGE ON SCHEMA public TO wallet_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE wallets, wager_transactions, inbox_messages, outbox_events, reference_retry_state TO wallet_app;
GRANT SELECT, INSERT ON TABLE wallet_ledger_entries TO wallet_app;

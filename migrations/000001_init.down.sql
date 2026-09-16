REVOKE ALL ON TABLE wallet_ledger_entries FROM wallet_app;
REVOKE ALL ON TABLE wallets, wager_transactions, inbox_messages, outbox_events, reference_retry_state FROM wallet_app;
REVOKE USAGE ON SCHEMA public FROM wallet_app;
REVOKE CONNECT ON DATABASE wallet FROM wallet_app;

DROP TABLE IF EXISTS reference_retry_state;
DROP TABLE IF EXISTS outbox_events;
DROP TABLE IF EXISTS inbox_messages;
DROP TRIGGER IF EXISTS wallet_ledger_append_only ON wallet_ledger_entries;
DROP FUNCTION IF EXISTS forbid_ledger_mutation();
DROP TABLE IF EXISTS wallet_ledger_entries;
DROP TABLE IF EXISTS wager_transactions;
DROP TABLE IF EXISTS wallets;

DROP ROLE IF EXISTS wallet_app;

-- +goose Up
CREATE TABLE IF NOT EXISTS balances (
    user_id BIGINT NOT NULL,
    currency TEXT NOT NULL,
    amount BIGINT NOT NULL CHECK (amount >= 0),
    PRIMARY KEY (user_id, currency),
    CHECK (currency = 'USDT')
);

CREATE TABLE IF NOT EXISTS withdrawals (
    id UUID PRIMARY KEY,
    user_id BIGINT NOT NULL,
    amount BIGINT NOT NULL CHECK (amount > 0),
    currency TEXT NOT NULL CHECK (currency = 'USDT'),
    destination TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status = 'pending'),
    idempotency_key TEXT NOT NULL,
    payload_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, idempotency_key)
);

CREATE TABLE IF NOT EXISTS idempotency_keys (
    user_id BIGINT NOT NULL,
    idempotency_key TEXT NOT NULL,
    payload_hash TEXT NOT NULL,
    withdrawal_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, idempotency_key),
    FOREIGN KEY (withdrawal_id) REFERENCES withdrawals(id)
);

-- +goose Down
DROP TABLE IF EXISTS idempotency_keys;
DROP TABLE IF EXISTS withdrawals;
DROP TABLE IF EXISTS balances;

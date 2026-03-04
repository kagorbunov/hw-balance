-- +goose Up
ALTER TABLE withdrawals DROP CONSTRAINT IF EXISTS withdrawals_status_check;
ALTER TABLE withdrawals ADD CONSTRAINT withdrawals_status_check CHECK (status IN ('pending', 'confirmed', 'cancelled'));

CREATE TABLE IF NOT EXISTS balance_ledger (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL,
    currency TEXT NOT NULL CHECK (currency = 'USDT'),
    amount BIGINT NOT NULL,
    balance_after BIGINT NOT NULL,
    operation_type TEXT NOT NULL CHECK (operation_type IN ('withdrawal_create', 'withdrawal_cancel')),
    withdrawal_id UUID REFERENCES withdrawals(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_balance_ledger_user_id ON balance_ledger(user_id);
CREATE INDEX idx_balance_ledger_withdrawal_id ON balance_ledger(withdrawal_id);

-- +goose Down
DROP TABLE IF EXISTS balance_ledger;
ALTER TABLE withdrawals DROP CONSTRAINT IF EXISTS withdrawals_status_check;
ALTER TABLE withdrawals ADD CONSTRAINT withdrawals_status_check CHECK (status = 'pending');

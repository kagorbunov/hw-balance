-- +goose Up
CREATE INDEX idx_withdrawals_user_id ON withdrawals(user_id);

-- +goose Down
DROP INDEX IF EXISTS idx_withdrawals_user_id;

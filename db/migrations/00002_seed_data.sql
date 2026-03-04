-- +goose Up
INSERT INTO balances (user_id, currency, amount)
VALUES (1, 'USDT', 1000000)
ON CONFLICT (user_id, currency) DO NOTHING;

-- +goose Down
DELETE FROM balances WHERE user_id = 1 AND currency = 'USDT';

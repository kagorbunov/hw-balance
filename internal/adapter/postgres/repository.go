package postgres

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"hw-balance/internal/model"
)

type WithdrawalRepository struct {
	db     *sql.DB
	logger *slog.Logger
}

type OperationType string

const (
	OperationWithdrawalCreate OperationType = "withdrawal_create"
	OperationWithdrawalCancel OperationType = "withdrawal_cancel"
)

func NewWithdrawalRepository(db *sql.DB, logger *slog.Logger) *WithdrawalRepository {
	return &WithdrawalRepository{db: db, logger: logger}
}

func (r *WithdrawalRepository) CreateWithdrawal(ctx context.Context, input model.CreateWithdrawalInput, payloadHash string) (model.Withdrawal, bool, error) {
	r.logger.Debug("начало транзакции вывода", "пользователь", input.UserID, "сумма", input.Amount)

	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		r.logger.Error("ошибка открытия транзакции", "error", err)
		return model.Withdrawal{}, false, err
	}
	defer tx.Rollback()

	idempotencyInsertTag, err := tx.ExecContext(ctx,
		`INSERT INTO idempotency_keys (user_id, idempotency_key, payload_hash) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		input.UserID,
		input.IdempotencyKey,
		payloadHash,
	)
	if err != nil {
		r.logger.Error("ошибка вставки ключа идемпотентности", "error", err, "пользователь", input.UserID)
		return model.Withdrawal{}, false, err
	}

	rowsAffected, err := idempotencyInsertTag.RowsAffected()
	if err != nil {
		r.logger.Error("ошибка получения количества затронутых строк", "error", err)
		return model.Withdrawal{}, false, err
	}

	if rowsAffected == 0 {
		r.logger.Warn("обнаружен повторный запрос по ключу идемпотентности", "пользователь", input.UserID, "ключ", input.IdempotencyKey)
		withdrawal, err := r.loadIdempotentWithdrawal(ctx, tx, input.UserID, input.IdempotencyKey, payloadHash)
		if err != nil {
			return model.Withdrawal{}, false, err
		}
		if err := tx.Commit(); err != nil {
			r.logger.Error("ошибка фиксации транзакции при идемпотентном ответе", "error", err)
			return model.Withdrawal{}, false, err
		}
		return withdrawal, true, nil
	}

	var currentBalance int64
	err = tx.QueryRowContext(ctx,
		`SELECT amount FROM balances WHERE user_id = $1 AND currency = $2 FOR UPDATE`,
		input.UserID,
		string(input.Currency),
	).Scan(&currentBalance)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Withdrawal{}, false, model.ErrInsufficientBalance
	}
	if err != nil {
		r.logger.Error("ошибка чтения баланса", "error", err, "пользователь", input.UserID)
		return model.Withdrawal{}, false, err
	}
	if currentBalance < input.Amount {
		return model.Withdrawal{}, false, model.ErrInsufficientBalance
	}

	var balanceAfter int64
	err = tx.QueryRowContext(ctx,
		`UPDATE balances SET amount = amount - $3 WHERE user_id = $1 AND currency = $2 RETURNING amount`,
		input.UserID,
		string(input.Currency),
		input.Amount,
	).Scan(&balanceAfter)
	if err != nil {
		r.logger.Error("ошибка обновления баланса", "error", err, "пользователь", input.UserID)
		return model.Withdrawal{}, false, err
	}

	withdrawal := model.Withdrawal{
		ID:             uuid.New(),
		UserID:         input.UserID,
		Amount:         input.Amount,
		Currency:       input.Currency,
		Destination:    input.Destination,
		Status:         model.WithdrawalStatusPending,
		IdempotencyKey: input.IdempotencyKey,
		CreatedAt:      time.Now().UTC(),
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO withdrawals (id, user_id, amount, currency, destination, status, idempotency_key, payload_hash, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		withdrawal.ID.String(),
		withdrawal.UserID,
		withdrawal.Amount,
		string(withdrawal.Currency),
		withdrawal.Destination,
		string(withdrawal.Status),
		withdrawal.IdempotencyKey,
		payloadHash,
		withdrawal.CreatedAt,
	)
	if err != nil {
		r.logger.Error("ошибка вставки записи вывода", "error", err, "пользователь", input.UserID)
		return model.Withdrawal{}, false, err
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE idempotency_keys SET withdrawal_id = $3 WHERE user_id = $1 AND idempotency_key = $2`,
		input.UserID,
		input.IdempotencyKey,
		withdrawal.ID.String(),
	)
	if err != nil {
		r.logger.Error("ошибка привязки withdrawal_id к ключу идемпотентности", "error", err, "пользователь", input.UserID)
		return model.Withdrawal{}, false, err
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO balance_ledger (user_id, currency, amount, balance_after, operation_type, withdrawal_id)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		withdrawal.UserID,
		string(withdrawal.Currency),
		-withdrawal.Amount,
		balanceAfter,
		string(OperationWithdrawalCreate),
		withdrawal.ID.String(),
	)
	if err != nil {
		r.logger.Error("ошибка записи в ledger", "error", err, "пользователь", input.UserID)
		return model.Withdrawal{}, false, err
	}

	if err := tx.Commit(); err != nil {
		r.logger.Error("ошибка фиксации транзакции", "error", err, "пользователь", input.UserID)
		return model.Withdrawal{}, false, err
	}

	r.logger.Info("вывод сохранён", "id", withdrawal.ID, "пользователь", withdrawal.UserID, "сумма", withdrawal.Amount)
	return withdrawal, false, nil
}

func (r *WithdrawalRepository) GetWithdrawalByID(ctx context.Context, id uuid.UUID) (model.Withdrawal, error) {
	r.logger.Debug("запрос вывода из базы данных", "id", id)
	withdrawal, err := r.getWithdrawalByID(ctx, r.db, id.String())
	if err != nil {
		return model.Withdrawal{}, err
	}
	return withdrawal, nil
}

func (r *WithdrawalRepository) loadIdempotentWithdrawal(
	ctx context.Context,
	tx *sql.Tx,
	userID int64,
	idempotencyKey string,
	payloadHash string,
) (model.Withdrawal, error) {
	var storedPayloadHash string
	var withdrawalID sql.NullString

	err := tx.QueryRowContext(ctx,
		`SELECT payload_hash, withdrawal_id FROM idempotency_keys WHERE user_id = $1 AND idempotency_key = $2 FOR UPDATE`,
		userID,
		idempotencyKey,
	).Scan(&storedPayloadHash, &withdrawalID)
	if err != nil {
		r.logger.Error("ошибка чтения записи идемпотентности", "error", err, "пользователь", userID)
		return model.Withdrawal{}, err
	}

	if storedPayloadHash != payloadHash {
		return model.Withdrawal{}, model.ErrIdempotencyPayloadMismatch
	}

	if !withdrawalID.Valid {
		return model.Withdrawal{}, model.ErrWithdrawalInProgress
	}

	withdrawal, err := r.getWithdrawalByID(ctx, tx, withdrawalID.String)
	if err != nil {
		return model.Withdrawal{}, err
	}

	return withdrawal, nil
}

type withdrawalReader interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (r *WithdrawalRepository) ConfirmWithdrawal(ctx context.Context, id uuid.UUID) (model.Withdrawal, error) {
	r.logger.Debug("подтверждение вывода", "id", id)

	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		r.logger.Error("ошибка открытия транзакции", "error", err)
		return model.Withdrawal{}, err
	}
	defer tx.Rollback()

	var withdrawal model.Withdrawal
	var withdrawalID, currency, status string

	err = tx.QueryRowContext(ctx,
		`SELECT id, user_id, amount, currency, destination, status, idempotency_key, created_at
		 FROM withdrawals WHERE id = $1 FOR UPDATE`,
		id.String(),
	).Scan(
		&withdrawalID,
		&withdrawal.UserID,
		&withdrawal.Amount,
		&currency,
		&withdrawal.Destination,
		&status,
		&withdrawal.IdempotencyKey,
		&withdrawal.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Withdrawal{}, model.ErrWithdrawalNotFound
	}
	if err != nil {
		r.logger.Error("ошибка чтения записи вывода", "error", err, "id", id)
		return model.Withdrawal{}, err
	}

	if status != string(model.WithdrawalStatusPending) {
		return model.Withdrawal{}, model.ErrWithdrawalNotPending
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE withdrawals SET status = $2 WHERE id = $1`,
		id.String(),
		string(model.WithdrawalStatusConfirmed),
	)
	if err != nil {
		r.logger.Error("ошибка обновления статуса вывода", "error", err, "id", id)
		return model.Withdrawal{}, err
	}

	if err := tx.Commit(); err != nil {
		r.logger.Error("ошибка фиксации транзакции", "error", err, "id", id)
		return model.Withdrawal{}, err
	}

	parsedID, _ := uuid.Parse(withdrawalID)
	withdrawal.ID = parsedID
	withdrawal.Currency = model.Currency(currency)
	withdrawal.Status = model.WithdrawalStatusConfirmed

	r.logger.Info("вывод подтверждён", "id", id)
	return withdrawal, nil
}

func (r *WithdrawalRepository) CancelWithdrawal(ctx context.Context, id uuid.UUID) (model.Withdrawal, error) {
	r.logger.Debug("отмена вывода", "id", id)

	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		r.logger.Error("ошибка открытия транзакции", "error", err)
		return model.Withdrawal{}, err
	}
	defer tx.Rollback()

	var withdrawal model.Withdrawal
	var withdrawalID, currency, status string

	err = tx.QueryRowContext(ctx,
		`SELECT id, user_id, amount, currency, destination, status, idempotency_key, created_at
		 FROM withdrawals WHERE id = $1 FOR UPDATE`,
		id.String(),
	).Scan(
		&withdrawalID,
		&withdrawal.UserID,
		&withdrawal.Amount,
		&currency,
		&withdrawal.Destination,
		&status,
		&withdrawal.IdempotencyKey,
		&withdrawal.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Withdrawal{}, model.ErrWithdrawalNotFound
	}
	if err != nil {
		r.logger.Error("ошибка чтения записи вывода", "error", err, "id", id)
		return model.Withdrawal{}, err
	}

	if status != string(model.WithdrawalStatusPending) {
		return model.Withdrawal{}, model.ErrWithdrawalNotPending
	}

	var balanceAfter int64
	err = tx.QueryRowContext(ctx,
		`UPDATE balances SET amount = amount + $3 WHERE user_id = $1 AND currency = $2 RETURNING amount`,
		withdrawal.UserID,
		currency,
		withdrawal.Amount,
	).Scan(&balanceAfter)
	if err != nil {
		r.logger.Error("ошибка возврата средств на баланс", "error", err, "id", id)
		return model.Withdrawal{}, err
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE withdrawals SET status = $2 WHERE id = $1`,
		id.String(),
		string(model.WithdrawalStatusCancelled),
	)
	if err != nil {
		r.logger.Error("ошибка обновления статуса вывода", "error", err, "id", id)
		return model.Withdrawal{}, err
	}

	parsedID, _ := uuid.Parse(withdrawalID)
	withdrawal.ID = parsedID
	withdrawal.Currency = model.Currency(currency)

	_, err = tx.ExecContext(ctx,
		`INSERT INTO balance_ledger (user_id, currency, amount, balance_after, operation_type, withdrawal_id)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		withdrawal.UserID,
		currency,
		withdrawal.Amount,
		balanceAfter,
		string(OperationWithdrawalCancel),
		withdrawal.ID.String(),
	)
	if err != nil {
		r.logger.Error("ошибка записи в ledger", "error", err, "id", id)
		return model.Withdrawal{}, err
	}

	if err := tx.Commit(); err != nil {
		r.logger.Error("ошибка фиксации транзакции", "error", err, "id", id)
		return model.Withdrawal{}, err
	}

	withdrawal.Status = model.WithdrawalStatusCancelled

	r.logger.Info("вывод отменён", "id", id)
	return withdrawal, nil
}

func (r *WithdrawalRepository) getWithdrawalByID(ctx context.Context, reader withdrawalReader, id string) (model.Withdrawal, error) {
	var withdrawal model.Withdrawal
	var withdrawalID string
	var currency string
	var status string

	err := reader.QueryRowContext(ctx,
		`SELECT id, user_id, amount, currency, destination, status, idempotency_key, created_at
		 FROM withdrawals
		 WHERE id = $1`,
		id,
	).Scan(
		&withdrawalID,
		&withdrawal.UserID,
		&withdrawal.Amount,
		&currency,
		&withdrawal.Destination,
		&status,
		&withdrawal.IdempotencyKey,
		&withdrawal.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Withdrawal{}, model.ErrWithdrawalNotFound
	}
	if err != nil {
		r.logger.Error("ошибка чтения записи вывода", "error", err, "id", id)
		return model.Withdrawal{}, err
	}

	parsedID, err := uuid.Parse(withdrawalID)
	if err != nil {
		r.logger.Error("ошибка разбора UUID вывода", "error", err, "id", withdrawalID)
		return model.Withdrawal{}, err
	}

	withdrawal.ID = parsedID
	withdrawal.Currency = model.Currency(currency)
	withdrawal.Status = model.WithdrawalStatus(status)

	return withdrawal, nil
}

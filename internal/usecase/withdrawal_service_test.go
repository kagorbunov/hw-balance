package usecase

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/google/uuid"

	"hw-balance/internal/model"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestCreateWithdrawalSuccess(t *testing.T) {
	t.Parallel()

	repo := newMemoryWithdrawalRepository()
	repo.setBalance(1, model.CurrencyUSDT, 1000)

	service := NewWithdrawalService(repo, discardLogger())
	result, err := service.CreateWithdrawal(context.Background(), model.CreateWithdrawalInput{
		UserID:         1,
		Amount:         300,
		Currency:       model.CurrencyUSDT,
		Destination:    "wallet-1",
		IdempotencyKey: "idem-success",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsIdempotencyReplay {
		t.Fatalf("expected new withdrawal, got replay")
	}
	if result.Withdrawal.Status != model.WithdrawalStatusPending {
		t.Fatalf("unexpected status: %s", result.Withdrawal.Status)
	}
	if result.Withdrawal.Amount != 300 {
		t.Fatalf("unexpected amount: %d", result.Withdrawal.Amount)
	}
	if repo.balance(1, model.CurrencyUSDT) != 700 {
		t.Fatalf("unexpected balance: %d", repo.balance(1, model.CurrencyUSDT))
	}
}

func TestCreateWithdrawalInsufficientBalance(t *testing.T) {
	t.Parallel()

	repo := newMemoryWithdrawalRepository()
	repo.setBalance(1, model.CurrencyUSDT, 100)

	service := NewWithdrawalService(repo, discardLogger())
	_, err := service.CreateWithdrawal(context.Background(), model.CreateWithdrawalInput{
		UserID:         1,
		Amount:         200,
		Currency:       model.CurrencyUSDT,
		Destination:    "wallet-2",
		IdempotencyKey: "idem-insufficient",
	})
	if !errors.Is(err, model.ErrInsufficientBalance) {
		t.Fatalf("expected insufficient balance, got: %v", err)
	}
}

func TestCreateWithdrawalIdempotency(t *testing.T) {
	t.Parallel()

	repo := newMemoryWithdrawalRepository()
	repo.setBalance(1, model.CurrencyUSDT, 1000)

	service := NewWithdrawalService(repo, discardLogger())
	first, err := service.CreateWithdrawal(context.Background(), model.CreateWithdrawalInput{
		UserID:         1,
		Amount:         400,
		Currency:       model.CurrencyUSDT,
		Destination:    "wallet-3",
		IdempotencyKey: "idem-repeat",
	})
	if err != nil {
		t.Fatalf("unexpected error on first call: %v", err)
	}

	second, err := service.CreateWithdrawal(context.Background(), model.CreateWithdrawalInput{
		UserID:         1,
		Amount:         400,
		Currency:       model.CurrencyUSDT,
		Destination:    "wallet-3",
		IdempotencyKey: "idem-repeat",
	})
	if err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}
	if !second.IsIdempotencyReplay {
		t.Fatalf("expected idempotent replay response")
	}
	if first.Withdrawal.ID != second.Withdrawal.ID {
		t.Fatalf("expected same withdrawal id, got %s and %s", first.Withdrawal.ID, second.Withdrawal.ID)
	}
	if repo.balance(1, model.CurrencyUSDT) != 600 {
		t.Fatalf("unexpected balance after replay: %d", repo.balance(1, model.CurrencyUSDT))
	}

	_, err = service.CreateWithdrawal(context.Background(), model.CreateWithdrawalInput{
		UserID:         1,
		Amount:         300,
		Currency:       model.CurrencyUSDT,
		Destination:    "wallet-3",
		IdempotencyKey: "idem-repeat",
	})
	if !errors.Is(err, model.ErrIdempotencyPayloadMismatch) {
		t.Fatalf("expected idempotency payload mismatch, got: %v", err)
	}
}

func TestCreateWithdrawalConcurrencySingleBalance(t *testing.T) {
	t.Parallel()

	repo := newMemoryWithdrawalRepository()
	repo.setBalance(1, model.CurrencyUSDT, 100)
	service := NewWithdrawalService(repo, discardLogger())

	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	create := func(key string) {
		defer wg.Done()
		_, err := service.CreateWithdrawal(context.Background(), model.CreateWithdrawalInput{
			UserID:         1,
			Amount:         80,
			Currency:       model.CurrencyUSDT,
			Destination:    "wallet-c",
			IdempotencyKey: key,
		})
		errCh <- err
	}

	wg.Add(2)
	go create("concurrent-1")
	go create("concurrent-2")
	wg.Wait()
	close(errCh)

	var successCount int
	var insufficientCount int
	for err := range errCh {
		if err == nil {
			successCount++
			continue
		}
		if errors.Is(err, model.ErrInsufficientBalance) {
			insufficientCount++
			continue
		}
		t.Fatalf("unexpected error: %v", err)
	}

	if successCount != 1 || insufficientCount != 1 {
		t.Fatalf("unexpected outcomes: success=%d insufficient=%d", successCount, insufficientCount)
	}
	if repo.balance(1, model.CurrencyUSDT) != 20 {
		t.Fatalf("unexpected final balance: %d", repo.balance(1, model.CurrencyUSDT))
	}
	if repo.withdrawalCount() != 1 {
		t.Fatalf("unexpected withdrawals count: %d", repo.withdrawalCount())
	}
}

type idempotencyRecord struct {
	payloadHash string
	withdrawal  model.Withdrawal
}

type memoryWithdrawalRepository struct {
	mu          sync.Mutex
	balances    map[int64]map[model.Currency]int64
	idempotency map[int64]map[string]idempotencyRecord
	withdrawals map[uuid.UUID]model.Withdrawal
}

func newMemoryWithdrawalRepository() *memoryWithdrawalRepository {
	return &memoryWithdrawalRepository{
		balances:    make(map[int64]map[model.Currency]int64),
		idempotency: make(map[int64]map[string]idempotencyRecord),
		withdrawals: make(map[uuid.UUID]model.Withdrawal),
	}
}

func (r *memoryWithdrawalRepository) CreateWithdrawal(ctx context.Context, input model.CreateWithdrawalInput, payloadHash string) (model.Withdrawal, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.idempotency[input.UserID]; !ok {
		r.idempotency[input.UserID] = make(map[string]idempotencyRecord)
	}

	record, exists := r.idempotency[input.UserID][input.IdempotencyKey]
	if exists {
		if record.payloadHash != payloadHash {
			return model.Withdrawal{}, false, model.ErrIdempotencyPayloadMismatch
		}
		return record.withdrawal, true, nil
	}

	currentBalance := r.balances[input.UserID][input.Currency]
	if currentBalance < input.Amount {
		return model.Withdrawal{}, false, model.ErrInsufficientBalance
	}

	r.balances[input.UserID][input.Currency] = currentBalance - input.Amount

	withdrawal := model.Withdrawal{
		ID:             uuid.New(),
		UserID:         input.UserID,
		Amount:         input.Amount,
		Currency:       input.Currency,
		Destination:    input.Destination,
		Status:         model.WithdrawalStatusPending,
		IdempotencyKey: input.IdempotencyKey,
	}
	r.withdrawals[withdrawal.ID] = withdrawal
	r.idempotency[input.UserID][input.IdempotencyKey] = idempotencyRecord{
		payloadHash: payloadHash,
		withdrawal:  withdrawal,
	}

	return withdrawal, false, nil
}

func (r *memoryWithdrawalRepository) GetWithdrawalByID(ctx context.Context, id uuid.UUID) (model.Withdrawal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	withdrawal, ok := r.withdrawals[id]
	if !ok {
		return model.Withdrawal{}, model.ErrWithdrawalNotFound
	}
	return withdrawal, nil
}

func (r *memoryWithdrawalRepository) ConfirmWithdrawal(ctx context.Context, id uuid.UUID) (model.Withdrawal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	withdrawal, ok := r.withdrawals[id]
	if !ok {
		return model.Withdrawal{}, model.ErrWithdrawalNotFound
	}
	if withdrawal.Status != model.WithdrawalStatusPending {
		return model.Withdrawal{}, model.ErrWithdrawalNotPending
	}
	withdrawal.Status = model.WithdrawalStatusConfirmed
	r.withdrawals[id] = withdrawal
	return withdrawal, nil
}

func (r *memoryWithdrawalRepository) CancelWithdrawal(ctx context.Context, id uuid.UUID) (model.Withdrawal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	withdrawal, ok := r.withdrawals[id]
	if !ok {
		return model.Withdrawal{}, model.ErrWithdrawalNotFound
	}
	if withdrawal.Status != model.WithdrawalStatusPending {
		return model.Withdrawal{}, model.ErrWithdrawalNotPending
	}
	r.balances[withdrawal.UserID][withdrawal.Currency] += withdrawal.Amount
	withdrawal.Status = model.WithdrawalStatusCancelled
	r.withdrawals[id] = withdrawal
	return withdrawal, nil
}

func (r *memoryWithdrawalRepository) setBalance(userID int64, currency model.Currency, amount int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.balances[userID]; !ok {
		r.balances[userID] = make(map[model.Currency]int64)
	}
	r.balances[userID][currency] = amount
}

func (r *memoryWithdrawalRepository) balance(userID int64, currency model.Currency) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.balances[userID][currency]
}

func (r *memoryWithdrawalRepository) withdrawalCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return len(r.withdrawals)
}

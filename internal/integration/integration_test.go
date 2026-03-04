package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	_ "github.com/lib/pq"

	adapterpostgres "hw-balance/internal/adapter/postgres"
	controllerhttp "hw-balance/internal/controller/http"
	"hw-balance/internal/dto"
	"hw-balance/internal/usecase"
)

const testAuthToken = "test-token"

type testEnv struct {
	db      *sql.DB
	server  *httptest.Server
	cleanup func()
}

func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping integration tests")
	}

	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatalf("failed to connect to database: %v", err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatalf("failed to ping database: %v", err)
	}

	if err := resetDatabase(db); err != nil {
		db.Close()
		t.Fatalf("failed to reset database: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repository := adapterpostgres.NewWithdrawalRepository(db, logger)
	service := usecase.NewWithdrawalService(repository, logger)
	handler := controllerhttp.NewHandler(testAuthToken, service, logger)
	healthHandler := controllerhttp.NewHealthHandler(db)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler.Liveness)
	mux.HandleFunc("GET /readyz", healthHandler.Readiness)
	mux.Handle("/", handler.Router())

	server := httptest.NewServer(mux)

	return &testEnv{
		db:     db,
		server: server,
		cleanup: func() {
			server.Close()
			db.Close()
		},
	}
}

func resetDatabase(db *sql.DB) error {
	queries := []string{
		"DELETE FROM idempotency_keys",
		"DELETE FROM withdrawals",
		"DELETE FROM balances",
		"INSERT INTO balances (user_id, currency, amount) VALUES (1, 'USDT', 1000000) ON CONFLICT (user_id, currency) DO UPDATE SET amount = 1000000",
	}

	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			return fmt.Errorf("query %q failed: %w", q, err)
		}
	}

	return nil
}

func (e *testEnv) setBalance(t *testing.T, userID int64, amount int64) {
	t.Helper()
	_, err := e.db.Exec(
		"UPDATE balances SET amount = $2 WHERE user_id = $1 AND currency = 'USDT'",
		userID, amount,
	)
	if err != nil {
		t.Fatalf("failed to set balance: %v", err)
	}
}

func (e *testEnv) getBalance(t *testing.T, userID int64) int64 {
	t.Helper()
	var amount int64
	err := e.db.QueryRow(
		"SELECT amount FROM balances WHERE user_id = $1 AND currency = 'USDT'",
		userID,
	).Scan(&amount)
	if err != nil {
		t.Fatalf("failed to get balance: %v", err)
	}
	return amount
}

func (e *testEnv) countWithdrawals(t *testing.T) int {
	t.Helper()
	var count int
	err := e.db.QueryRow("SELECT COUNT(*) FROM withdrawals").Scan(&count)
	if err != nil {
		t.Fatalf("failed to count withdrawals: %v", err)
	}
	return count
}

func (e *testEnv) createWithdrawal(t *testing.T, req dto.CreateWithdrawalRequest) (*http.Response, dto.WithdrawalResponse) {
	t.Helper()

	body, _ := json.Marshal(req)
	httpReq, _ := http.NewRequest(http.MethodPost, e.server.URL+"/v1/withdrawals", bytes.NewReader(body))
	httpReq.Header.Set("Authorization", "Bearer "+testAuthToken)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	var result dto.WithdrawalResponse
	if resp.StatusCode == http.StatusCreated {
		json.NewDecoder(resp.Body).Decode(&result)
	}
	resp.Body.Close()

	return resp, result
}

func (e *testEnv) getWithdrawal(t *testing.T, id string) (*http.Response, dto.WithdrawalResponse) {
	t.Helper()

	httpReq, _ := http.NewRequest(http.MethodGet, e.server.URL+"/v1/withdrawals/"+id, nil)
	httpReq.Header.Set("Authorization", "Bearer "+testAuthToken)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	var result dto.WithdrawalResponse
	if resp.StatusCode == http.StatusOK {
		json.NewDecoder(resp.Body).Decode(&result)
	}
	resp.Body.Close()

	return resp, result
}

func TestCreateWithdrawalSuccess(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	env.setBalance(t, 1, 10000)

	resp, result := env.createWithdrawal(t, dto.CreateWithdrawalRequest{
		UserID:         1,
		Amount:         500,
		Currency:       "USDT",
		Destination:    "wallet-success-test",
		IdempotencyKey: "success-key-1",
	})

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	if result.Status != "pending" {
		t.Fatalf("expected status pending, got %s", result.Status)
	}

	if result.Amount != 500 {
		t.Fatalf("expected amount 500, got %d", result.Amount)
	}

	if env.getBalance(t, 1) != 9500 {
		t.Fatalf("expected balance 9500, got %d", env.getBalance(t, 1))
	}
}

func TestCreateWithdrawalInsufficientBalance(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	env.setBalance(t, 1, 100)

	resp, _ := env.createWithdrawal(t, dto.CreateWithdrawalRequest{
		UserID:         1,
		Amount:         500,
		Currency:       "USDT",
		Destination:    "wallet-insufficient",
		IdempotencyKey: "insufficient-key-1",
	})

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}

	if env.getBalance(t, 1) != 100 {
		t.Fatalf("expected balance unchanged at 100, got %d", env.getBalance(t, 1))
	}
}

func TestCreateWithdrawalInvalidAmount(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	testCases := []struct {
		name   string
		amount int64
	}{
		{"zero amount", 0},
		{"negative amount", -100},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp, _ := env.createWithdrawal(t, dto.CreateWithdrawalRequest{
				UserID:         1,
				Amount:         tc.amount,
				Currency:       "USDT",
				Destination:    "wallet-invalid",
				IdempotencyKey: fmt.Sprintf("invalid-amount-%d", tc.amount),
			})

			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", resp.StatusCode)
			}
		})
	}
}

func TestCreateWithdrawalIdempotencySamePayload(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	env.setBalance(t, 1, 10000)

	req := dto.CreateWithdrawalRequest{
		UserID:         1,
		Amount:         300,
		Currency:       "USDT",
		Destination:    "wallet-idempotent",
		IdempotencyKey: "idem-same-payload",
	}

	resp1, result1 := env.createWithdrawal(t, req)
	if resp1.StatusCode != http.StatusCreated {
		t.Fatalf("first request: expected 201, got %d", resp1.StatusCode)
	}

	resp2, result2 := env.createWithdrawal(t, req)
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("second request: expected 201, got %d", resp2.StatusCode)
	}

	if result1.ID != result2.ID {
		t.Fatalf("expected same withdrawal ID, got %s and %s", result1.ID, result2.ID)
	}

	if env.getBalance(t, 1) != 9700 {
		t.Fatalf("expected balance 9700, got %d", env.getBalance(t, 1))
	}

	if env.countWithdrawals(t) != 1 {
		t.Fatalf("expected 1 withdrawal, got %d", env.countWithdrawals(t))
	}
}

func TestCreateWithdrawalIdempotencyDifferentPayload(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	env.setBalance(t, 1, 10000)

	resp1, _ := env.createWithdrawal(t, dto.CreateWithdrawalRequest{
		UserID:         1,
		Amount:         300,
		Currency:       "USDT",
		Destination:    "wallet-1",
		IdempotencyKey: "idem-different-payload",
	})
	if resp1.StatusCode != http.StatusCreated {
		t.Fatalf("first request: expected 201, got %d", resp1.StatusCode)
	}

	resp2, _ := env.createWithdrawal(t, dto.CreateWithdrawalRequest{
		UserID:         1,
		Amount:         500,
		Currency:       "USDT",
		Destination:    "wallet-1",
		IdempotencyKey: "idem-different-payload",
	})
	if resp2.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("second request with different payload: expected 422, got %d", resp2.StatusCode)
	}

	if env.getBalance(t, 1) != 9700 {
		t.Fatalf("expected balance 9700, got %d", env.getBalance(t, 1))
	}
}

func TestCreateWithdrawalConcurrency(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	env.setBalance(t, 1, 150)

	const numWorkers = 10
	results := make(chan int, numWorkers)

	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for i := 0; i < numWorkers; i++ {
		go func(idx int) {
			defer wg.Done()

			resp, _ := env.createWithdrawal(t, dto.CreateWithdrawalRequest{
				UserID:         1,
				Amount:         100,
				Currency:       "USDT",
				Destination:    "wallet-concurrent",
				IdempotencyKey: fmt.Sprintf("concurrent-key-%d", idx),
			})
			results <- resp.StatusCode
		}(i)
	}

	wg.Wait()
	close(results)

	var successCount, conflictCount, otherCount int
	for status := range results {
		switch status {
		case http.StatusCreated:
			successCount++
		case http.StatusConflict:
			conflictCount++
		default:
			otherCount++
		}
	}

	if successCount != 1 {
		t.Fatalf("expected exactly 1 success, got %d", successCount)
	}

	if conflictCount != numWorkers-1 {
		t.Fatalf("expected %d conflicts, got %d", numWorkers-1, conflictCount)
	}

	if otherCount != 0 {
		t.Fatalf("unexpected status codes: %d", otherCount)
	}

	if env.getBalance(t, 1) != 50 {
		t.Fatalf("expected balance 50, got %d", env.getBalance(t, 1))
	}

	if env.countWithdrawals(t) != 1 {
		t.Fatalf("expected 1 withdrawal, got %d", env.countWithdrawals(t))
	}
}

func TestGetWithdrawal(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	env.setBalance(t, 1, 10000)

	_, created := env.createWithdrawal(t, dto.CreateWithdrawalRequest{
		UserID:         1,
		Amount:         200,
		Currency:       "USDT",
		Destination:    "wallet-get-test",
		IdempotencyKey: "get-withdrawal-key",
	})

	resp, result := env.getWithdrawal(t, created.ID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if result.ID != created.ID {
		t.Fatalf("expected ID %s, got %s", created.ID, result.ID)
	}

	if result.Amount != 200 {
		t.Fatalf("expected amount 200, got %d", result.Amount)
	}
}

func TestGetWithdrawalNotFound(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	resp, _ := env.getWithdrawal(t, "00000000-0000-0000-0000-000000000000")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestGetWithdrawalInvalidID(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	resp, _ := env.getWithdrawal(t, "not-a-uuid")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestUnauthorizedAccess(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	testCases := []struct {
		name   string
		token  string
		method string
		path   string
	}{
		{"no token POST", "", http.MethodPost, "/v1/withdrawals"},
		{"wrong token POST", "wrong-token", http.MethodPost, "/v1/withdrawals"},
		{"no token GET", "", http.MethodGet, "/v1/withdrawals/some-id"},
		{"wrong token GET", "wrong-token", http.MethodGet, "/v1/withdrawals/some-id"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequestWithContext(
				context.Background(),
				tc.method,
				env.server.URL+tc.path,
				bytes.NewReader([]byte("{}")),
			)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			req.Header.Set("Content-Type", "application/json")

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			resp.Body.Close()

			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", resp.StatusCode)
			}
		})
	}
}

func TestHealthEndpoints(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	t.Run("liveness", func(t *testing.T) {
		resp, err := http.Get(env.server.URL + "/healthz")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
	})

	t.Run("readiness", func(t *testing.T) {
		resp, err := http.Get(env.server.URL + "/readyz")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
	})
}

func TestValidationErrors(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	testCases := []struct {
		name string
		req  dto.CreateWithdrawalRequest
	}{
		{
			name: "empty destination",
			req: dto.CreateWithdrawalRequest{
				UserID:         1,
				Amount:         100,
				Currency:       "USDT",
				Destination:    "",
				IdempotencyKey: "valid-key",
			},
		},
		{
			name: "empty idempotency key",
			req: dto.CreateWithdrawalRequest{
				UserID:         1,
				Amount:         100,
				Currency:       "USDT",
				Destination:    "wallet",
				IdempotencyKey: "",
			},
		},
		{
			name: "invalid currency",
			req: dto.CreateWithdrawalRequest{
				UserID:         1,
				Amount:         100,
				Currency:       "BTC",
				Destination:    "wallet",
				IdempotencyKey: "valid-key",
			},
		},
		{
			name: "invalid user id",
			req: dto.CreateWithdrawalRequest{
				UserID:         0,
				Amount:         100,
				Currency:       "USDT",
				Destination:    "wallet",
				IdempotencyKey: "valid-key",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp, _ := env.createWithdrawal(t, tc.req)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", resp.StatusCode)
			}
		})
	}
}

func TestConcurrentIdempotentRequests(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	env.setBalance(t, 1, 10000)

	const numWorkers = 10
	results := make(chan struct {
		status int
		id     string
	}, numWorkers)

	var wg sync.WaitGroup
	wg.Add(numWorkers)

	req := dto.CreateWithdrawalRequest{
		UserID:         1,
		Amount:         500,
		Currency:       "USDT",
		Destination:    "wallet-concurrent-idem",
		IdempotencyKey: "same-idem-key-for-all",
	}

	for i := 0; i < numWorkers; i++ {
		go func() {
			defer wg.Done()
			resp, result := env.createWithdrawal(t, req)
			results <- struct {
				status int
				id     string
			}{resp.StatusCode, result.ID}
		}()
	}

	wg.Wait()
	close(results)

	var successCount int
	var withdrawalID string
	for r := range results {
		if r.status == http.StatusCreated {
			successCount++
			if withdrawalID == "" {
				withdrawalID = r.id
			} else if withdrawalID != r.id {
				t.Fatalf("expected same withdrawal ID for all, got different: %s and %s", withdrawalID, r.id)
			}
		} else {
			t.Fatalf("unexpected status %d", r.status)
		}
	}

	if successCount != numWorkers {
		t.Fatalf("expected all %d requests to succeed, got %d", numWorkers, successCount)
	}

	if env.getBalance(t, 1) != 9500 {
		t.Fatalf("expected balance 9500, got %d", env.getBalance(t, 1))
	}

	if env.countWithdrawals(t) != 1 {
		t.Fatalf("expected 1 withdrawal, got %d", env.countWithdrawals(t))
	}
}

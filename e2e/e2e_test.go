package e2e

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

type createWithdrawalRequest struct {
	UserID         int64  `json:"user_id"`
	Amount         int64  `json:"amount"`
	Currency       string `json:"currency"`
	Destination    string `json:"destination"`
	IdempotencyKey string `json:"idempotency_key"`
}

type withdrawalResponse struct {
	ID          string `json:"id"`
	UserID      int64  `json:"user_id"`
	Amount      int64  `json:"amount"`
	Currency    string `json:"currency"`
	Destination string `json:"destination"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type e2eEnv struct {
	baseURL   string
	authToken string
	db        *sql.DB
}

func setupE2E(t *testing.T) *e2eEnv {
	t.Helper()

	baseURL := os.Getenv("E2E_BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:18088"
	}

	authToken := os.Getenv("E2E_AUTH_TOKEN")
	if authToken == "" {
		authToken = "secret-token"
	}

	databaseURL := os.Getenv("E2E_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://app:app@localhost:5432/hw_balance?sslmode=disable"
	}

	if err := waitForServer(baseURL, 30*time.Second); err != nil {
		t.Fatalf("server not ready: %v", err)
	}

	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatalf("failed to connect to database: %v", err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatalf("failed to ping database: %v", err)
	}

	env := &e2eEnv{
		baseURL:   baseURL,
		authToken: authToken,
		db:        db,
	}

	if err := env.resetDatabase(); err != nil {
		db.Close()
		t.Fatalf("failed to reset database: %v", err)
	}

	t.Cleanup(func() {
		db.Close()
	})

	return env
}

func waitForServer(baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("server did not become ready within %v", timeout)
}

func (e *e2eEnv) resetDatabase() error {
	queries := []string{
		"DELETE FROM idempotency_keys",
		"DELETE FROM withdrawals",
		"DELETE FROM balances",
		"INSERT INTO balances (user_id, currency, amount) VALUES (1, 'USDT', 1000000) ON CONFLICT (user_id, currency) DO UPDATE SET amount = 1000000",
	}

	for _, q := range queries {
		if _, err := e.db.Exec(q); err != nil {
			return fmt.Errorf("query %q failed: %w", q, err)
		}
	}

	return nil
}

func (e *e2eEnv) setBalance(t *testing.T, userID int64, amount int64) {
	t.Helper()
	_, err := e.db.Exec(
		"UPDATE balances SET amount = $2 WHERE user_id = $1 AND currency = 'USDT'",
		userID, amount,
	)
	if err != nil {
		t.Fatalf("failed to set balance: %v", err)
	}
}

func (e *e2eEnv) getBalance(t *testing.T, userID int64) int64 {
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

func (e *e2eEnv) countWithdrawals(t *testing.T) int {
	t.Helper()
	var count int
	err := e.db.QueryRow("SELECT COUNT(*) FROM withdrawals").Scan(&count)
	if err != nil {
		t.Fatalf("failed to count withdrawals: %v", err)
	}
	return count
}

func (e *e2eEnv) doRequest(method, path string, body interface{}, auth bool) (*http.Response, []byte, error) {
	var bodyReader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, e.baseURL+path, bodyReader)
	if err != nil {
		return nil, nil, err
	}

	if auth {
		req.Header.Set("Authorization", "Bearer "+e.authToken)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, nil, err
	}

	return resp, respBody, nil
}

func (e *e2eEnv) createWithdrawal(t *testing.T, req createWithdrawalRequest) (int, *withdrawalResponse, *errorResponse) {
	t.Helper()

	resp, body, err := e.doRequest(http.MethodPost, "/v1/withdrawals", req, true)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if resp.StatusCode == http.StatusCreated {
		var result withdrawalResponse
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		return resp.StatusCode, &result, nil
	}

	var errResp errorResponse
	json.Unmarshal(body, &errResp)
	return resp.StatusCode, nil, &errResp
}

func (e *e2eEnv) getWithdrawal(t *testing.T, id string) (int, *withdrawalResponse) {
	t.Helper()

	resp, body, err := e.doRequest(http.MethodGet, "/v1/withdrawals/"+id, nil, true)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if resp.StatusCode == http.StatusOK {
		var result withdrawalResponse
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		return resp.StatusCode, &result
	}

	return resp.StatusCode, nil
}

func TestE2E_HealthEndpoints(t *testing.T) {
	env := setupE2E(t)

	t.Run("liveness check", func(t *testing.T) {
		resp, _, err := env.doRequest(http.MethodGet, "/healthz", nil, false)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
	})

	t.Run("readiness check", func(t *testing.T) {
		resp, _, err := env.doRequest(http.MethodGet, "/readyz", nil, false)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
	})
}

func TestE2E_CreateWithdrawal_Success(t *testing.T) {
	env := setupE2E(t)
	env.setBalance(t, 1, 10000)

	status, result, _ := env.createWithdrawal(t, createWithdrawalRequest{
		UserID:         1,
		Amount:         500,
		Currency:       "USDT",
		Destination:    "TRC20-e2e-wallet-success",
		IdempotencyKey: fmt.Sprintf("e2e-success-%d", time.Now().UnixNano()),
	})

	if status != http.StatusCreated {
		t.Fatalf("expected 201, got %d", status)
	}

	if result.Status != "pending" {
		t.Fatalf("expected status pending, got %s", result.Status)
	}

	if result.Amount != 500 {
		t.Fatalf("expected amount 500, got %d", result.Amount)
	}

	if result.Currency != "USDT" {
		t.Fatalf("expected currency USDT, got %s", result.Currency)
	}

	balance := env.getBalance(t, 1)
	if balance != 9500 {
		t.Fatalf("expected balance 9500, got %d", balance)
	}
}

func TestE2E_CreateWithdrawal_InsufficientBalance(t *testing.T) {
	env := setupE2E(t)
	env.setBalance(t, 1, 100)

	status, _, errResp := env.createWithdrawal(t, createWithdrawalRequest{
		UserID:         1,
		Amount:         500,
		Currency:       "USDT",
		Destination:    "TRC20-wallet-insufficient",
		IdempotencyKey: fmt.Sprintf("e2e-insufficient-%d", time.Now().UnixNano()),
	})

	if status != http.StatusConflict {
		t.Fatalf("expected 409, got %d", status)
	}

	if errResp == nil || errResp.Error == "" {
		t.Fatal("expected error message in response")
	}

	balance := env.getBalance(t, 1)
	if balance != 100 {
		t.Fatalf("expected balance unchanged at 100, got %d", balance)
	}
}

func TestE2E_CreateWithdrawal_InvalidAmount(t *testing.T) {
	env := setupE2E(t)

	testCases := []struct {
		name   string
		amount int64
	}{
		{"zero", 0},
		{"negative", -100},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			status, _, _ := env.createWithdrawal(t, createWithdrawalRequest{
				UserID:         1,
				Amount:         tc.amount,
				Currency:       "USDT",
				Destination:    "TRC20-wallet",
				IdempotencyKey: fmt.Sprintf("e2e-invalid-amount-%d-%d", tc.amount, time.Now().UnixNano()),
			})

			if status != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", status)
			}
		})
	}
}

func TestE2E_CreateWithdrawal_ValidationErrors(t *testing.T) {
	env := setupE2E(t)

	testCases := []struct {
		name string
		req  createWithdrawalRequest
	}{
		{
			name: "empty destination",
			req: createWithdrawalRequest{
				UserID:         1,
				Amount:         100,
				Currency:       "USDT",
				Destination:    "",
				IdempotencyKey: "valid-key",
			},
		},
		{
			name: "empty idempotency key",
			req: createWithdrawalRequest{
				UserID:         1,
				Amount:         100,
				Currency:       "USDT",
				Destination:    "wallet",
				IdempotencyKey: "",
			},
		},
		{
			name: "invalid currency",
			req: createWithdrawalRequest{
				UserID:         1,
				Amount:         100,
				Currency:       "BTC",
				Destination:    "wallet",
				IdempotencyKey: "valid-key-btc",
			},
		},
		{
			name: "zero user id",
			req: createWithdrawalRequest{
				UserID:         0,
				Amount:         100,
				Currency:       "USDT",
				Destination:    "wallet",
				IdempotencyKey: "valid-key-zero-user",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			status, _, _ := env.createWithdrawal(t, tc.req)
			if status != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", status)
			}
		})
	}
}

func TestE2E_Idempotency_SamePayload(t *testing.T) {
	env := setupE2E(t)
	env.setBalance(t, 1, 10000)

	key := fmt.Sprintf("e2e-idem-same-%d", time.Now().UnixNano())
	req := createWithdrawalRequest{
		UserID:         1,
		Amount:         300,
		Currency:       "USDT",
		Destination:    "TRC20-wallet-idempotent",
		IdempotencyKey: key,
	}

	status1, result1, _ := env.createWithdrawal(t, req)
	if status1 != http.StatusCreated {
		t.Fatalf("first request: expected 201, got %d", status1)
	}

	status2, result2, _ := env.createWithdrawal(t, req)
	if status2 != http.StatusCreated {
		t.Fatalf("second request: expected 201, got %d", status2)
	}

	if result1.ID != result2.ID {
		t.Fatalf("expected same withdrawal ID, got %s and %s", result1.ID, result2.ID)
	}

	balance := env.getBalance(t, 1)
	if balance != 9700 {
		t.Fatalf("expected balance 9700 (debited once), got %d", balance)
	}

	count := env.countWithdrawals(t)
	if count != 1 {
		t.Fatalf("expected 1 withdrawal, got %d", count)
	}
}

func TestE2E_Idempotency_DifferentPayload(t *testing.T) {
	env := setupE2E(t)
	env.setBalance(t, 1, 10000)

	key := fmt.Sprintf("e2e-idem-diff-%d", time.Now().UnixNano())

	status1, _, _ := env.createWithdrawal(t, createWithdrawalRequest{
		UserID:         1,
		Amount:         300,
		Currency:       "USDT",
		Destination:    "TRC20-wallet-1",
		IdempotencyKey: key,
	})
	if status1 != http.StatusCreated {
		t.Fatalf("first request: expected 201, got %d", status1)
	}

	status2, _, errResp := env.createWithdrawal(t, createWithdrawalRequest{
		UserID:         1,
		Amount:         500,
		Currency:       "USDT",
		Destination:    "TRC20-wallet-1",
		IdempotencyKey: key,
	})
	if status2 != http.StatusUnprocessableEntity {
		t.Fatalf("second request with different payload: expected 422, got %d", status2)
	}

	if errResp == nil || errResp.Error == "" {
		t.Fatal("expected error message for idempotency mismatch")
	}

	balance := env.getBalance(t, 1)
	if balance != 9700 {
		t.Fatalf("expected balance 9700, got %d", balance)
	}
}

func TestE2E_GetWithdrawal_Success(t *testing.T) {
	env := setupE2E(t)
	env.setBalance(t, 1, 10000)

	_, created, _ := env.createWithdrawal(t, createWithdrawalRequest{
		UserID:         1,
		Amount:         200,
		Currency:       "USDT",
		Destination:    "TRC20-wallet-get-test",
		IdempotencyKey: fmt.Sprintf("e2e-get-%d", time.Now().UnixNano()),
	})

	status, result := env.getWithdrawal(t, created.ID)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}

	if result.ID != created.ID {
		t.Fatalf("expected ID %s, got %s", created.ID, result.ID)
	}

	if result.Amount != 200 {
		t.Fatalf("expected amount 200, got %d", result.Amount)
	}

	if result.Status != "pending" {
		t.Fatalf("expected status pending, got %s", result.Status)
	}
}

func TestE2E_GetWithdrawal_NotFound(t *testing.T) {
	env := setupE2E(t)

	status, _ := env.getWithdrawal(t, "00000000-0000-0000-0000-000000000000")
	if status != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", status)
	}
}

func TestE2E_GetWithdrawal_InvalidID(t *testing.T) {
	env := setupE2E(t)

	status, _ := env.getWithdrawal(t, "invalid-uuid")
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", status)
	}
}

func TestE2E_Authentication(t *testing.T) {
	env := setupE2E(t)

	testCases := []struct {
		name   string
		token  string
		method string
		path   string
	}{
		{"no token POST", "", http.MethodPost, "/v1/withdrawals"},
		{"wrong token POST", "wrong-token", http.MethodPost, "/v1/withdrawals"},
		{"no token GET", "", http.MethodGet, "/v1/withdrawals/00000000-0000-0000-0000-000000000000"},
		{"wrong token GET", "wrong-token", http.MethodGet, "/v1/withdrawals/00000000-0000-0000-0000-000000000000"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var bodyReader io.Reader
			if tc.method == http.MethodPost {
				data, _ := json.Marshal(createWithdrawalRequest{
					UserID:         1,
					Amount:         100,
					Currency:       "USDT",
					Destination:    "wallet",
					IdempotencyKey: "auth-test-key",
				})
				bodyReader = bytes.NewReader(data)
			}

			req, _ := http.NewRequest(tc.method, env.baseURL+tc.path, bodyReader)
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

func TestE2E_Concurrency_MultipleWithdrawals(t *testing.T) {
	env := setupE2E(t)
	env.setBalance(t, 1, 150)

	const numWorkers = 10
	results := make(chan int, numWorkers)

	var wg sync.WaitGroup
	wg.Add(numWorkers)

	baseKey := fmt.Sprintf("e2e-concurrent-%d", time.Now().UnixNano())
	for i := 0; i < numWorkers; i++ {
		go func(idx int) {
			defer wg.Done()

			status, _, _ := env.createWithdrawal(t, createWithdrawalRequest{
				UserID:         1,
				Amount:         100,
				Currency:       "USDT",
				Destination:    "TRC20-wallet-concurrent",
				IdempotencyKey: fmt.Sprintf("%s-%d", baseKey, idx),
			})
			results <- status
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

	balance := env.getBalance(t, 1)
	if balance != 50 {
		t.Fatalf("expected balance 50, got %d", balance)
	}

	count := env.countWithdrawals(t)
	if count != 1 {
		t.Fatalf("expected 1 withdrawal, got %d", count)
	}
}

func TestE2E_Concurrency_IdempotentRequests(t *testing.T) {
	env := setupE2E(t)
	env.setBalance(t, 1, 10000)

	const numWorkers = 10
	type result struct {
		status int
		id     string
	}
	results := make(chan result, numWorkers)

	var wg sync.WaitGroup
	wg.Add(numWorkers)

	key := fmt.Sprintf("e2e-concurrent-idem-%d", time.Now().UnixNano())
	req := createWithdrawalRequest{
		UserID:         1,
		Amount:         500,
		Currency:       "USDT",
		Destination:    "TRC20-wallet-concurrent-idem",
		IdempotencyKey: key,
	}

	for i := 0; i < numWorkers; i++ {
		go func() {
			defer wg.Done()
			status, resp, _ := env.createWithdrawal(t, req)
			r := result{status: status}
			if resp != nil {
				r.id = resp.ID
			}
			results <- r
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
		t.Fatalf("expected all %d requests to succeed with same result, got %d", numWorkers, successCount)
	}

	balance := env.getBalance(t, 1)
	if balance != 9500 {
		t.Fatalf("expected balance 9500 (debited once), got %d", balance)
	}

	count := env.countWithdrawals(t)
	if count != 1 {
		t.Fatalf("expected 1 withdrawal, got %d", count)
	}
}

func TestE2E_FullFlow(t *testing.T) {
	env := setupE2E(t)
	env.setBalance(t, 1, 1000)

	key1 := fmt.Sprintf("e2e-flow-1-%d", time.Now().UnixNano())
	status1, w1, _ := env.createWithdrawal(t, createWithdrawalRequest{
		UserID:         1,
		Amount:         300,
		Currency:       "USDT",
		Destination:    "TRC20-wallet-flow-1",
		IdempotencyKey: key1,
	})
	if status1 != http.StatusCreated {
		t.Fatalf("first withdrawal: expected 201, got %d", status1)
	}
	if env.getBalance(t, 1) != 700 {
		t.Fatalf("after first withdrawal: expected balance 700, got %d", env.getBalance(t, 1))
	}

	statusGet, wGet := env.getWithdrawal(t, w1.ID)
	if statusGet != http.StatusOK {
		t.Fatalf("get withdrawal: expected 200, got %d", statusGet)
	}
	if wGet.Amount != 300 {
		t.Fatalf("get withdrawal: expected amount 300, got %d", wGet.Amount)
	}

	key2 := fmt.Sprintf("e2e-flow-2-%d", time.Now().UnixNano())
	status2, w2, _ := env.createWithdrawal(t, createWithdrawalRequest{
		UserID:         1,
		Amount:         200,
		Currency:       "USDT",
		Destination:    "TRC20-wallet-flow-2",
		IdempotencyKey: key2,
	})
	if status2 != http.StatusCreated {
		t.Fatalf("second withdrawal: expected 201, got %d", status2)
	}
	if w1.ID == w2.ID {
		t.Fatal("expected different withdrawal IDs")
	}
	if env.getBalance(t, 1) != 500 {
		t.Fatalf("after second withdrawal: expected balance 500, got %d", env.getBalance(t, 1))
	}

	status3, w3, _ := env.createWithdrawal(t, createWithdrawalRequest{
		UserID:         1,
		Amount:         300,
		Currency:       "USDT",
		Destination:    "TRC20-wallet-flow-1",
		IdempotencyKey: key1,
	})
	if status3 != http.StatusCreated {
		t.Fatalf("idempotent retry: expected 201, got %d", status3)
	}
	if w3.ID != w1.ID {
		t.Fatalf("idempotent retry: expected same ID %s, got %s", w1.ID, w3.ID)
	}
	if env.getBalance(t, 1) != 500 {
		t.Fatalf("after idempotent retry: balance should stay 500, got %d", env.getBalance(t, 1))
	}

	key4 := fmt.Sprintf("e2e-flow-4-%d", time.Now().UnixNano())
	status4, _, _ := env.createWithdrawal(t, createWithdrawalRequest{
		UserID:         1,
		Amount:         1000,
		Currency:       "USDT",
		Destination:    "TRC20-wallet-flow-4",
		IdempotencyKey: key4,
	})
	if status4 != http.StatusConflict {
		t.Fatalf("insufficient balance: expected 409, got %d", status4)
	}
	if env.getBalance(t, 1) != 500 {
		t.Fatalf("after failed withdrawal: balance should stay 500, got %d", env.getBalance(t, 1))
	}

	if env.countWithdrawals(t) != 2 {
		t.Fatalf("expected 2 withdrawals total, got %d", env.countWithdrawals(t))
	}
}

func TestE2E_NoInternalErrorsExposed(t *testing.T) {
	env := setupE2E(t)

	resp, body, err := env.doRequest(http.MethodPost, "/v1/withdrawals", map[string]interface{}{
		"user_id":         "not-a-number",
		"amount":          100,
		"currency":        "USDT",
		"destination":     "wallet",
		"idempotency_key": "test",
	}, true)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for malformed JSON, got %d", resp.StatusCode)
	}

	bodyStr := string(body)
	sensitivePatterns := []string{
		"stack trace",
		"panic",
		"runtime error",
		"sql:",
		"pq:",
		"postgres",
		"/go/src/",
	}

	for _, pattern := range sensitivePatterns {
		if bytes.Contains(body, []byte(pattern)) {
			t.Fatalf("response contains sensitive info %q: %s", pattern, bodyStr)
		}
	}
}

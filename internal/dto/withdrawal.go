package dto

type CreateWithdrawalRequest struct {
	UserID         int64  `json:"user_id"`
	Amount         int64  `json:"amount"`
	Currency       string `json:"currency"`
	Destination    string `json:"destination"`
	IdempotencyKey string `json:"idempotency_key"`
}

type WithdrawalResponse struct {
	ID          string `json:"id"`
	UserID      int64  `json:"user_id"`
	Amount      int64  `json:"amount"`
	Currency    string `json:"currency"`
	Destination string `json:"destination"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
}

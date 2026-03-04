package model

import (
	"time"

	"github.com/google/uuid"
)

type Currency string

const CurrencyUSDT Currency = "USDT"

type WithdrawalStatus string

const WithdrawalStatusPending WithdrawalStatus = "pending"

type Withdrawal struct {
	ID             uuid.UUID
	UserID         int64
	Amount         int64
	Currency       Currency
	Destination    string
	Status         WithdrawalStatus
	IdempotencyKey string
	CreatedAt      time.Time
}

type CreateWithdrawalInput struct {
	UserID         int64
	Amount         int64
	Currency       Currency
	Destination    string
	IdempotencyKey string
}

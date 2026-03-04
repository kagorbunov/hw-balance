package model

import "errors"

var (
	ErrInvalidAmount              = errors.New("invalid amount")
	ErrInvalidCurrency            = errors.New("invalid currency")
	ErrInvalidDestination         = errors.New("invalid destination")
	ErrMissingIdempotencyKey      = errors.New("missing idempotency key")
	ErrInvalidUserID              = errors.New("invalid user id")
	ErrInvalidWithdrawalID        = errors.New("invalid withdrawal id")
	ErrInsufficientBalance        = errors.New("insufficient balance")
	ErrIdempotencyPayloadMismatch = errors.New("idempotency payload mismatch")
	ErrWithdrawalNotFound         = errors.New("withdrawal not found")
	ErrWithdrawalInProgress       = errors.New("withdrawal in progress")
	ErrWithdrawalNotPending       = errors.New("withdrawal is not in pending status")
)

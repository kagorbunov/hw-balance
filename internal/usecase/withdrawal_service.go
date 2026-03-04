package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"hw-balance/internal/model"
)

const (
	maxRetries    = 5
	retryInterval = 50 * time.Millisecond
)

type WithdrawalRepository interface {
	CreateWithdrawal(ctx context.Context, input model.CreateWithdrawalInput, payloadHash string) (model.Withdrawal, bool, error)
	GetWithdrawalByID(ctx context.Context, id uuid.UUID) (model.Withdrawal, error)
	ConfirmWithdrawal(ctx context.Context, id uuid.UUID) (model.Withdrawal, error)
	CancelWithdrawal(ctx context.Context, id uuid.UUID) (model.Withdrawal, error)
}

type WithdrawalService struct {
	repo   WithdrawalRepository
	logger *slog.Logger
}

type CreateWithdrawalResult struct {
	Withdrawal          model.Withdrawal
	IsIdempotencyReplay bool
}

func NewWithdrawalService(repo WithdrawalRepository, logger *slog.Logger) *WithdrawalService {
	return &WithdrawalService{repo: repo, logger: logger}
}

func (s *WithdrawalService) CreateWithdrawal(ctx context.Context, input model.CreateWithdrawalInput) (CreateWithdrawalResult, error) {
	s.logger.Debug("создание вывода", "пользователь", input.UserID, "сумма", input.Amount, "валюта", input.Currency)

	if input.UserID <= 0 {
		s.logger.Warn("некорректный идентификатор пользователя", "пользователь", input.UserID)
		return CreateWithdrawalResult{}, model.ErrInvalidUserID
	}
	if input.Amount <= 0 {
		s.logger.Warn("некорректная сумма", "сумма", input.Amount)
		return CreateWithdrawalResult{}, model.ErrInvalidAmount
	}
	if input.Currency != model.CurrencyUSDT {
		s.logger.Warn("неподдерживаемая валюта", "валюта", input.Currency)
		return CreateWithdrawalResult{}, model.ErrInvalidCurrency
	}

	input.Destination = strings.TrimSpace(input.Destination)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)

	if input.Destination == "" {
		s.logger.Warn("пустой адрес назначения", "пользователь", input.UserID)
		return CreateWithdrawalResult{}, model.ErrInvalidDestination
	}
	if input.IdempotencyKey == "" {
		s.logger.Warn("отсутствует ключ идемпотентности", "пользователь", input.UserID)
		return CreateWithdrawalResult{}, model.ErrMissingIdempotencyKey
	}

	payloadHash := hashCreateWithdrawalPayload(input)

	var withdrawal model.Withdrawal
	var replay bool
	var err error

	for attempt := 0; attempt < maxRetries; attempt++ {
		withdrawal, replay, err = s.repo.CreateWithdrawal(ctx, input, payloadHash)
		if err == nil {
			break
		}
		if !errors.Is(err, model.ErrWithdrawalInProgress) {
			s.logger.Warn("ошибка создания вывода", "error", err, "пользователь", input.UserID)
			return CreateWithdrawalResult{}, err
		}
		s.logger.Debug("вывод в процессе создания, повтор", "попытка", attempt+1, "пользователь", input.UserID)
		select {
		case <-ctx.Done():
			return CreateWithdrawalResult{}, ctx.Err()
		case <-time.After(retryInterval):
		}
	}

	if err != nil {
		s.logger.Warn("ошибка создания вывода после повторов", "error", err, "пользователь", input.UserID)
		return CreateWithdrawalResult{}, err
	}

	s.logger.Info("вывод создан", "id", withdrawal.ID, "пользователь", withdrawal.UserID, "идемпотентный_повтор", replay)
	return CreateWithdrawalResult{
		Withdrawal:          withdrawal,
		IsIdempotencyReplay: replay,
	}, nil
}

func (s *WithdrawalService) GetWithdrawal(ctx context.Context, withdrawalID string) (model.Withdrawal, error) {
	s.logger.Debug("получение вывода", "id", withdrawalID)

	id, err := uuid.Parse(strings.TrimSpace(withdrawalID))
	if err != nil {
		s.logger.Warn("некорректный идентификатор вывода", "id", withdrawalID)
		return model.Withdrawal{}, model.ErrInvalidWithdrawalID
	}
	return s.repo.GetWithdrawalByID(ctx, id)
}

func (s *WithdrawalService) ConfirmWithdrawal(ctx context.Context, withdrawalID string) (model.Withdrawal, error) {
	s.logger.Debug("подтверждение вывода", "id", withdrawalID)

	id, err := uuid.Parse(strings.TrimSpace(withdrawalID))
	if err != nil {
		s.logger.Warn("некорректный идентификатор вывода", "id", withdrawalID)
		return model.Withdrawal{}, model.ErrInvalidWithdrawalID
	}

	withdrawal, err := s.repo.ConfirmWithdrawal(ctx, id)
	if err != nil {
		s.logger.Warn("ошибка подтверждения вывода", "error", err, "id", withdrawalID)
		return model.Withdrawal{}, err
	}

	s.logger.Info("вывод подтверждён", "id", withdrawal.ID)
	return withdrawal, nil
}

func (s *WithdrawalService) CancelWithdrawal(ctx context.Context, withdrawalID string) (model.Withdrawal, error) {
	s.logger.Debug("отмена вывода", "id", withdrawalID)

	id, err := uuid.Parse(strings.TrimSpace(withdrawalID))
	if err != nil {
		s.logger.Warn("некорректный идентификатор вывода", "id", withdrawalID)
		return model.Withdrawal{}, model.ErrInvalidWithdrawalID
	}

	withdrawal, err := s.repo.CancelWithdrawal(ctx, id)
	if err != nil {
		s.logger.Warn("ошибка отмены вывода", "error", err, "id", withdrawalID)
		return model.Withdrawal{}, err
	}

	s.logger.Info("вывод отменён", "id", withdrawal.ID)
	return withdrawal, nil
}

func hashCreateWithdrawalPayload(input model.CreateWithdrawalInput) string {
	raw := fmt.Sprintf("%d|%d|%s|%s", input.UserID, input.Amount, input.Currency, input.Destination)
	hash := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(hash[:])
}

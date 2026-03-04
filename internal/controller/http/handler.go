package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"hw-balance/internal/dto"
	"hw-balance/internal/model"
	"hw-balance/internal/usecase"
	"hw-balance/pkg/httputil"
)

type Handler struct {
	token   string
	service *usecase.WithdrawalService
	logger  *slog.Logger
}

func NewHandler(token string, service *usecase.WithdrawalService, logger *slog.Logger) *Handler {
	return &Handler{
		token:   token,
		service: service,
		logger:  logger,
	}
}

func (h *Handler) Router() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /v1/withdrawals", h.withAuth(http.HandlerFunc(h.createWithdrawal)))
	mux.Handle("GET /v1/withdrawals/{id}", h.withAuth(http.HandlerFunc(h.getWithdrawal)))
	mux.Handle("POST /v1/withdrawals/{id}/confirm", h.withAuth(http.HandlerFunc(h.confirmWithdrawal)))
	mux.Handle("POST /v1/withdrawals/{id}/cancel", h.withAuth(http.HandlerFunc(h.cancelWithdrawal)))
	return mux
}

func (h *Handler) createWithdrawal(w http.ResponseWriter, r *http.Request) {
	var request dto.CreateWithdrawalRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&request); err != nil {
		h.logger.Warn("ошибка разбора тела запроса", "error", err)
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	h.logger.Debug("создание вывода", "пользователь", request.UserID, "сумма", request.Amount)

	result, err := h.service.CreateWithdrawal(r.Context(), model.CreateWithdrawalInput{
		UserID:         request.UserID,
		Amount:         request.Amount,
		Currency:       model.Currency(request.Currency),
		Destination:    request.Destination,
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		h.writeUseCaseError(w, r, err)
		return
	}

	h.logger.Info("вывод создан", "id", result.Withdrawal.ID, "идемпотентный_повтор", result.IsIdempotencyReplay)

	response := dto.WithdrawalResponse{
		ID:          result.Withdrawal.ID.String(),
		UserID:      result.Withdrawal.UserID,
		Amount:      result.Withdrawal.Amount,
		Currency:    string(result.Withdrawal.Currency),
		Destination: result.Withdrawal.Destination,
		Status:      string(result.Withdrawal.Status),
		CreatedAt:   result.Withdrawal.CreatedAt.UTC().Format(http.TimeFormat),
	}
	httputil.WriteJSON(w, http.StatusCreated, response)
}

func (h *Handler) getWithdrawal(w http.ResponseWriter, r *http.Request) {
	withdrawalID := r.PathValue("id")
	h.logger.Debug("получение вывода", "id", withdrawalID)

	withdrawal, err := h.service.GetWithdrawal(r.Context(), withdrawalID)
	if err != nil {
		h.writeUseCaseError(w, r, err)
		return
	}

	h.logger.Info("вывод получен", "id", withdrawal.ID)

	response := dto.WithdrawalResponse{
		ID:          withdrawal.ID.String(),
		UserID:      withdrawal.UserID,
		Amount:      withdrawal.Amount,
		Currency:    string(withdrawal.Currency),
		Destination: withdrawal.Destination,
		Status:      string(withdrawal.Status),
		CreatedAt:   withdrawal.CreatedAt.UTC().Format(http.TimeFormat),
	}
	httputil.WriteJSON(w, http.StatusOK, response)
}

func (h *Handler) confirmWithdrawal(w http.ResponseWriter, r *http.Request) {
	withdrawalID := r.PathValue("id")
	h.logger.Debug("подтверждение вывода", "id", withdrawalID)

	withdrawal, err := h.service.ConfirmWithdrawal(r.Context(), withdrawalID)
	if err != nil {
		h.writeUseCaseError(w, r, err)
		return
	}

	h.logger.Info("вывод подтверждён", "id", withdrawal.ID)

	response := dto.WithdrawalResponse{
		ID:          withdrawal.ID.String(),
		UserID:      withdrawal.UserID,
		Amount:      withdrawal.Amount,
		Currency:    string(withdrawal.Currency),
		Destination: withdrawal.Destination,
		Status:      string(withdrawal.Status),
		CreatedAt:   withdrawal.CreatedAt.UTC().Format(http.TimeFormat),
	}
	httputil.WriteJSON(w, http.StatusOK, response)
}

func (h *Handler) cancelWithdrawal(w http.ResponseWriter, r *http.Request) {
	withdrawalID := r.PathValue("id")
	h.logger.Debug("отмена вывода", "id", withdrawalID)

	withdrawal, err := h.service.CancelWithdrawal(r.Context(), withdrawalID)
	if err != nil {
		h.writeUseCaseError(w, r, err)
		return
	}

	h.logger.Info("вывод отменён", "id", withdrawal.ID)

	response := dto.WithdrawalResponse{
		ID:          withdrawal.ID.String(),
		UserID:      withdrawal.UserID,
		Amount:      withdrawal.Amount,
		Currency:    string(withdrawal.Currency),
		Destination: withdrawal.Destination,
		Status:      string(withdrawal.Status),
		CreatedAt:   withdrawal.CreatedAt.UTC().Format(http.TimeFormat),
	}
	httputil.WriteJSON(w, http.StatusOK, response)
}

func (h *Handler) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.authorized(r) {
			h.logger.Warn("неавторизованный запрос", "метод", r.Method, "путь", r.URL.Path)
			httputil.WriteError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) authorized(r *http.Request) bool {
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if authHeader == "" {
		return false
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(authHeader, prefix) {
		return false
	}
	token := strings.TrimSpace(strings.TrimPrefix(authHeader, prefix))
	return token != "" && token == h.token
}

func (h *Handler) writeUseCaseError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, model.ErrInvalidAmount),
		errors.Is(err, model.ErrInvalidCurrency),
		errors.Is(err, model.ErrInvalidDestination),
		errors.Is(err, model.ErrMissingIdempotencyKey),
		errors.Is(err, model.ErrInvalidUserID),
		errors.Is(err, model.ErrInvalidWithdrawalID):
		h.logger.Warn("ошибка валидации запроса", "error", err, "метод", r.Method, "путь", r.URL.Path)
		httputil.WriteError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, model.ErrInsufficientBalance):
		h.logger.Warn("недостаточно средств", "метод", r.Method, "путь", r.URL.Path)
		httputil.WriteError(w, http.StatusConflict, model.ErrInsufficientBalance.Error())
	case errors.Is(err, model.ErrIdempotencyPayloadMismatch):
		h.logger.Warn("несоответствие данных идемпотентного запроса", "метод", r.Method, "путь", r.URL.Path)
		httputil.WriteError(w, http.StatusUnprocessableEntity, model.ErrIdempotencyPayloadMismatch.Error())
	case errors.Is(err, model.ErrWithdrawalNotFound):
		h.logger.Warn("вывод не найден", "метод", r.Method, "путь", r.URL.Path)
		httputil.WriteError(w, http.StatusNotFound, model.ErrWithdrawalNotFound.Error())
	case errors.Is(err, model.ErrWithdrawalInProgress):
		h.logger.Warn("вывод в процессе создания", "метод", r.Method, "путь", r.URL.Path)
		httputil.WriteError(w, http.StatusConflict, "withdrawal in progress, please retry")
	case errors.Is(err, model.ErrWithdrawalNotPending):
		h.logger.Warn("вывод не в статусе pending", "метод", r.Method, "путь", r.URL.Path)
		httputil.WriteError(w, http.StatusConflict, model.ErrWithdrawalNotPending.Error())
	default:
		h.logger.Error("внутренняя ошибка сервиса", "error", err, "метод", r.Method, "путь", r.URL.Path)
		httputil.WriteError(w, http.StatusInternalServerError, "internal server error")
	}
}

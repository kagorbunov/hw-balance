package health

import (
	"context"
	"net/http"
	"time"
)

type Pinger interface {
	PingContext(ctx context.Context) error
}

type Handler struct {
	pinger  Pinger
	timeout time.Duration
}

func NewHandler(pinger Pinger) *Handler {
	return &Handler{
		pinger:  pinger,
		timeout: 2 * time.Second,
	}
}

func NewHandlerWithTimeout(pinger Pinger, timeout time.Duration) *Handler {
	return &Handler{
		pinger:  pinger,
		timeout: timeout,
	}
}

func (h *Handler) Liveness(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (h *Handler) Readiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	if err := h.pinger.PingContext(ctx); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("database unavailable"))
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

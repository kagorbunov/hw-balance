package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	adapterpostgres "hw-balance/internal/adapter/postgres"
	controllerhttp "hw-balance/internal/controller/http"
	"hw-balance/internal/usecase"
)

const (
	defaultPort            = "8080"
	defaultShutdownTimeout = 30 * time.Second
	readHeaderTimeout      = 5 * time.Second
	readTimeout            = 10 * time.Second
	writeTimeout           = 30 * time.Second
	idleTimeout            = 60 * time.Second
	dbConnectTimeout       = 10 * time.Second
)

func main() {
	os.Exit(run())
}

func run() int {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLogLevel()}))
	slog.SetDefault(logger)

	cfg, err := loadConfig()
	if err != nil {
		logger.Error("failed to load config", "error", err)
		return 1
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	db, err := adapterpostgres.NewDB(cfg.databaseURL)
	if err != nil {
		logger.Error("failed to create postgres connection", "error", err)
		return 1
	}
	defer db.Close()

	pingCtx, pingCancel := context.WithTimeout(ctx, dbConnectTimeout)
	defer pingCancel()

	if err := db.PingContext(pingCtx); err != nil {
		logger.Error("failed to ping postgres", "error", err)
		return 1
	}

	repository := adapterpostgres.NewWithdrawalRepository(db, logger)
	service := usecase.NewWithdrawalService(repository, logger)
	handler := controllerhttp.NewHandler(cfg.authToken, service, logger)
	healthHandler := controllerhttp.NewHealthHandler(db)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler.Liveness)
	mux.HandleFunc("GET /readyz", healthHandler.Readiness)
	mux.Handle("/", handler.Router())

	server := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("server started", "addr", ":"+cfg.port)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		logger.Error("server failed", "error", err)
		return 1
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
	defer shutdownCancel()

	logger.Info("shutting down server", "timeout", defaultShutdownTimeout)

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("server shutdown failed", "error", err)
		return 1
	}

	logger.Info("server stopped gracefully")
	return 0
}

type config struct {
	databaseURL string
	authToken   string
	port        string
}

func loadConfig() (*config, error) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return nil, errors.New("DATABASE_URL is required")
	}

	authToken := os.Getenv("AUTH_TOKEN")
	if authToken == "" {
		return nil, errors.New("AUTH_TOKEN is required")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	return &config{
		databaseURL: databaseURL,
		authToken:   authToken,
		port:        port,
	}, nil
}

func parseLogLevel() slog.Level {
	switch os.Getenv("LOG_LEVEL") {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

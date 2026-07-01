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

	"github.com/garv2003/code-execution-engine/internal/config"
	"github.com/garv2003/code-execution-engine/internal/handler"

	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)
	slog.Info("Starting Code Execution Engine API Server...")
	cfg, err := config.Load()
	if err != nil {
		slog.Error("Failed to load configuration", "error", err)
		os.Exit(1)
	}
	slog.Info("Configuration loaded successfully", "port", cfg.Port, "max_workers", cfg.MaxWorkers)

	resultHandler := handler.NewResultHandler()
	submitHandler := handler.NewSubmitHandler()
	healthHandler := handler.NewHealthHandler()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", healthHandler.Health)
	mux.HandleFunc("POST /submit", submitHandler.Submit)
	mux.HandleFunc("GET /result/{id}", resultHandler.Result)

	serverAddr := ":" + cfg.Port
	srv := &http.Server{
		Addr:              serverAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErrors := make(chan error, 1)
	go func() {
		slog.Info("Server is listening", "addr", serverAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()

	shutdownSignal := make(chan os.Signal, 1)
	signal.Notify(shutdownSignal, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErrors:
		slog.Error("Server error occurred, initiating shutdown", "error", err)
	case sig := <-shutdownSignal:
		slog.Info("Shutdown signal received", "signal", sig.String())

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		slog.Info("Shutting down server gracefully...")
		if err := srv.Shutdown(ctx); err != nil {
			slog.Error("Graceful shutdown failed, forcing close", "error", err)
			if err := srv.Close(); err != nil {
				slog.Error("Error forcing server close", "error", err)
			}
		} else {
			slog.Info("Server shutdown complete.")
		}
	}
}

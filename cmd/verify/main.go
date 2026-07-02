package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/garv2003/code-execution-engine/internal/config"
	"github.com/garv2003/code-execution-engine/internal/sandbox"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	slog.Info("Loading configuration for runtime verification...")
	cfg, err := config.Load()
	if err != nil {
		slog.Error("Configuration error", "error", err)
		os.Exit(1)
	}

	langConfig, err := sandbox.LoadLanguageConfig(cfg.LanguageConfig)
	if err != nil {
		slog.Error("Failed to load languages configuration", "error", err)
		os.Exit(1)
	}

	dockerSandbox, err := sandbox.NewDockerSandbox(cfg, langConfig)
	if err != nil {
		slog.Error("Docker client initialization failure", "error", err)
		os.Exit(1)
	}

	slog.Info("Starting runtime health verification...")
	ctx := context.Background()
	
	if err := dockerSandbox.VerifyRuntimes(ctx); err != nil {
		slog.Error("Verify failed", "error", err)
		os.Exit(1)
	}

	slog.Info("All runtimes verified successfully.")
}

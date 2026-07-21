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
	"github.com/garv2003/code-execution-engine/internal/db"
	"github.com/garv2003/code-execution-engine/internal/handler"
	"github.com/garv2003/code-execution-engine/internal/metrics"
	"github.com/garv2003/code-execution-engine/internal/middleware"
	"github.com/garv2003/code-execution-engine/internal/pushsub"
	"github.com/garv2003/code-execution-engine/internal/sandbox"
	"github.com/garv2003/code-execution-engine/internal/worker"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	slog.Info("Initializing Code Execution Engine...")

	cfg, err := config.Load()
	if err != nil {
		slog.Error("Configuration error", "error", err)
		os.Exit(1)
	}

	redisClient, err := pushsub.NewRedisClient(cfg.RedisURL)
	if err != nil {
		slog.Error("Redis connection failure", "error", err)
		os.Exit(1)
	}
	slog.Info("Connected to Redis successfully")

	pgDB, err := db.NewPostgresDB(cfg.DatabaseURL)
	if err != nil {
		slog.Error("Postgres connection failure", "error", err)
		os.Exit(1)
	}

	langConfig, err := sandbox.LoadLanguageConfig(cfg.LanguageConfig)
	if err != nil {
		slog.Error("Failed to load languages configuration", "error", err)
		os.Exit(1)
	}

	supportedLanguages := make(map[string]bool)
	languagesList := make([]string, 0, len(langConfig))
	for lang := range langConfig {
		supportedLanguages[lang] = true
		languagesList = append(languagesList, lang)
	}

	mode := getEnv("APP_MODE", "both")
	slog.Info("Running mode selected", "mode", mode)

	shutdownSignal := make(chan os.Signal, 2)
	signal.Notify(shutdownSignal, os.Interrupt, syscall.SIGTERM)

	var workerPool *worker.WorkerPool
	var dockerSandbox *sandbox.DockerSandbox

	if mode == "worker" || mode == "both" {
		var activeSandbox sandbox.Sandbox

		slog.Info("Selecting sandbox backend", "backend", cfg.SandboxBackend)
		switch cfg.SandboxBackend {
		case "native":
			nativeSandbox, err := sandbox.NewNativeSandbox(cfg, langConfig)
			if err != nil {
				slog.Error("Native sandbox initialization failure", "error", err)
				os.Exit(1)
			}
			activeSandbox = nativeSandbox
		default:
			ds, err := sandbox.NewDockerSandbox(cfg, langConfig)
			if err != nil {
				slog.Error("Docker client initialization failure", "error", err)
				os.Exit(1)
			}
			dockerSandbox = ds

			hotLanguages := languagesList
			if len(cfg.PrePullLanguages) > 0 {
				hotLanguages = cfg.PrePullLanguages
			}

			if cfg.PrePullImages {
				go func() {
					pullCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
					defer cancel()
					_ = dockerSandbox.PrePullImages(pullCtx, hotLanguages)
					// Warm pool is a no-op unless WARM_POOL_ENABLED; pre-fill
					// after images are present so warm containers can start.
					dockerSandbox.WarmUp(context.Background(), hotLanguages)
				}()
			} else if cfg.WarmPoolEnabled {
				go func() {
					warmCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
					defer cancel()
					dockerSandbox.WarmUp(warmCtx, hotLanguages)
				}()
			}
			activeSandbox = ds
		}

		workerPool = worker.NewWorkerPool(redisClient, activeSandbox, pgDB, cfg.MaxWorkers)
		workerPool.Start()
	}

	var srv *http.Server

	if mode == "api" || mode == "both" {
		submitHandler := handler.NewSubmitHandler(redisClient, pgDB, langConfig)
		resultHandler := handler.NewResultHandler(redisClient)
		healthHandler := handler.NewHealthHandler()
		jobHandler := handler.NewJobHandler(redisClient)
		dashboardHandler := handler.NewDashboardHandler(pgDB)

		mux := http.NewServeMux()
		mux.HandleFunc("GET /health", healthHandler.Health)
		mux.Handle("GET /metrics", metrics.Handler())
		mux.HandleFunc("POST /submit", submitHandler.Submit)
		mux.HandleFunc("GET /result/{id}", resultHandler.Result)
		mux.HandleFunc("GET /jobs/{id}", jobHandler.Job)
		mux.HandleFunc("GET /dashboard/jobs", dashboardHandler.Jobs)
		if cfg.PlaygroundEnabled {
			mux.Handle("GET /playground/", http.StripPrefix("/playground/", http.FileServer(http.Dir(cfg.PlaygroundDir))))
			mux.Handle("GET /playground", http.RedirectHandler("/playground/", http.StatusMovedPermanently))
		}

		var apiHandler http.Handler = mux
		apiHandler = middleware.NewRateLimiter(cfg.RateLimitRPM).Limit(apiHandler)
		apiHandler = middleware.CORS(cfg.CORSAllowedOrigins, cfg.CORSAllowedMethods, cfg.CORSAllowedHeaders, apiHandler)
		apiHandler = middleware.APIKey(cfg.APIKeys, apiHandler)

		serverAddr := ":" + cfg.Port
		srv = &http.Server{
			Addr:              serverAddr,
			Handler:           apiHandler,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
		}

		go func() {
			slog.Info("API Server listening", "addr", serverAddr)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("HTTP Server error", "error", err)
				shutdownSignal <- syscall.SIGTERM
			}
		}()
	}

	sig := <-shutdownSignal
	slog.Info("Shutdown signal captured", "signal", sig.String())
	go func() {
		sig := <-shutdownSignal
		slog.Warn("Force shutdown signal captured", "signal", sig.String())
		os.Exit(1)
	}()

	if srv != nil {
		slog.Info("Stopping API server gracefully...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			slog.Error("API Server forced shutdown", "error", err)
			_ = srv.Close()
		}
		slog.Info("API server stopped.")
	}

	if workerPool != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := workerPool.Stop(ctx); err != nil {
			slog.Error("Worker pool forced shutdown", "error", err)
		}
	}

	if dockerSandbox != nil {
		dockerSandbox.Close()
	}

	slog.Info("System shutdown completed successfully.")
}

func getEnv(key, defaultValue string) string {
	if val, exists := os.LookupEnv(key); exists {
		return val
	}
	return defaultValue
}

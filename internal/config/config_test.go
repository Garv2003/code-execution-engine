package config_test

import (
	"os"
	"testing"

	"github.com/garv2003/code-execution-engine/internal/config"
)

func TestLoad_Defaults(t *testing.T) {
	os.Clearenv()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Port != "8080" {
		t.Errorf("expected port 8080, got %s", cfg.Port)
	}
	if cfg.MaxWorkers != 10 {
		t.Errorf("expected 10 workers, got %d", cfg.MaxWorkers)
	}
	if cfg.DockerTimeoutMS != 5000 {
		t.Errorf("expected 5000ms timeout, got %d", cfg.DockerTimeoutMS)
	}
	if cfg.LanguageConfig != "languages.json" {
		t.Errorf("expected default language config, got %s", cfg.LanguageConfig)
	}
	if !cfg.PlaygroundEnabled {
		t.Error("expected playground to be enabled by default")
	}
	if cfg.RateLimitRPM != 60 {
		t.Errorf("expected default rate limit 60, got %d", cfg.RateLimitRPM)
	}
}

func TestLoad_CustomValues(t *testing.T) {
	os.Clearenv()
	os.Setenv("PORT", "9090")
	os.Setenv("MAX_WORKERS", "20")
	os.Setenv("DOCKER_TIMEOUT_MS", "10000")
	os.Setenv("REDIS_URL", "redis://custom:6379")
	os.Setenv("LANGUAGES_CONFIG", "custom-languages.json")
	os.Setenv("PLAYGROUND_ENABLED", "false")
	os.Setenv("RATE_LIMIT_RPM", "120")
	os.Setenv("CORS_ALLOWED_ORIGINS", "https://example.com,https://app.example.com")
	os.Setenv("PRE_PULL_LANGUAGES", "python,cpp")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Port != "9090" {
		t.Errorf("expected port 9090, got %s", cfg.Port)
	}
	if cfg.MaxWorkers != 20 {
		t.Errorf("expected 20 workers, got %d", cfg.MaxWorkers)
	}
	if cfg.RedisURL != "redis://custom:6379" {
		t.Errorf("expected custom redis url, got %s", cfg.RedisURL)
	}
	if cfg.LanguageConfig != "custom-languages.json" {
		t.Errorf("expected custom language config, got %s", cfg.LanguageConfig)
	}
	if cfg.PlaygroundEnabled {
		t.Error("expected playground to be disabled")
	}
	if cfg.RateLimitRPM != 120 {
		t.Errorf("expected rate limit 120, got %d", cfg.RateLimitRPM)
	}
	if len(cfg.CORSAllowedOrigins) != 2 {
		t.Fatalf("expected 2 cors origins, got %d", len(cfg.CORSAllowedOrigins))
	}
	if cfg.CORSAllowedOrigins[0] != "https://example.com" {
		t.Errorf("expected first cors origin, got %s", cfg.CORSAllowedOrigins[0])
	}
	if len(cfg.PrePullLanguages) != 2 || cfg.PrePullLanguages[1] != "cpp" {
		t.Errorf("expected parsed pre-pull languages, got %#v", cfg.PrePullLanguages)
	}
}

func TestLoad_InvalidWorkers(t *testing.T) {
	os.Clearenv()
	os.Setenv("MAX_WORKERS", "abc")

	_, err := config.Load()
	if err == nil {
		t.Error("expected error for invalid MAX_WORKERS, got nil")
	}
}

func TestValidate_ZeroWorkers(t *testing.T) {
	os.Clearenv()
	os.Setenv("MAX_WORKERS", "0")

	_, err := config.Load()
	if err == nil {
		t.Error("expected validation error for zero workers, got nil")
	}
}

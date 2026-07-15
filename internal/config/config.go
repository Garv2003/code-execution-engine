package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Port               string
	MaxWorkers         int
	DockerTimeoutMS    int
	RedisURL           string
	LanguageConfig     string
	PlaygroundEnabled  bool
	PlaygroundDir      string
	PrePullImages      bool
	PrePullLanguages   []string
	RateLimitRPM       int
	APIKeys            []string
	CORSAllowedOrigins []string
	CORSAllowedMethods []string
	CORSAllowedHeaders []string
	DockerMemoryLimit    string
	DockerCPUPeriod      int64
	DockerCPUQuota       int64
	DockerPidsLimit      int64
	DockerReadonlyRootfs bool
	DockerTmpfsSizeMB    int64
	MaxOutputBytes       int64
	DockerRuntime        string
	DatabaseURL          string
}

func Load() (*Config, error) {
	port := getEnv("PORT", "8080")
	redisURL := getEnv("REDIS_URL", "redis://localhost:6379")
	languageConfig := getEnv("LANGUAGES_CONFIG", "languages.json")
	playgroundDir := getEnv("PLAYGROUND_DIR", "playground")
	dockerMemLimit := getEnv("DOCKER_MEMORY_LIMIT", "128m")

	maxWorkers, err := getEnvInt("MAX_WORKERS", 10)
	if err != nil {
		return nil, fmt.Errorf("invalid MAX_WORKERS: %w", err)
	}

	rateLimitRPM, err := getEnvInt("RATE_LIMIT_RPM", 60)
	if err != nil {
		return nil, fmt.Errorf("invalid RATE_LIMIT_RPM: %w", err)
	}

	dockerTimeout, err := getEnvInt("DOCKER_TIMEOUT_MS", 5000)
	if err != nil {
		return nil, fmt.Errorf("invalid DOCKER_TIMEOUT_MS: %w", err)
	}

	dockerCPUPeriod, err := getEnvInt64("DOCKER_CPU_PERIOD", 100000)
	if err != nil {
		return nil, fmt.Errorf("invalid DOCKER_CPU_PERIOD: %w", err)
	}

	dockerCPUQuota, err := getEnvInt64("DOCKER_CPU_QUOTA", 50000)
	if err != nil {
		return nil, fmt.Errorf("invalid DOCKER_CPU_QUOTA: %w", err)
	}

	dockerPidsLimit, err := getEnvInt64("DOCKER_PIDS_LIMIT", 256)
	if err != nil {
		return nil, fmt.Errorf("invalid DOCKER_PIDS_LIMIT: %w", err)
	}

	dockerTmpfsSizeMB, err := getEnvInt64("DOCKER_TMPFS_SIZE_MB", 64)
	if err != nil {
		return nil, fmt.Errorf("invalid DOCKER_TMPFS_SIZE_MB: %w", err)
	}

	maxOutputBytes, err := getEnvInt64("MAX_OUTPUT_BYTES", 1048576)
	if err != nil {
		return nil, fmt.Errorf("invalid MAX_OUTPUT_BYTES: %w", err)
	}

	cfg := &Config{
		Port:               port,
		MaxWorkers:         maxWorkers,
		DockerTimeoutMS:    dockerTimeout,
		RedisURL:           redisURL,
		LanguageConfig:     languageConfig,
		PlaygroundEnabled:  getEnvBool("PLAYGROUND_ENABLED", true),
		PlaygroundDir:      playgroundDir,
		PrePullImages:      getEnvBool("PRE_PULL_IMAGES", true),
		PrePullLanguages:   getEnvCSV("PRE_PULL_LANGUAGES", ""),
		RateLimitRPM:       rateLimitRPM,
		APIKeys:            getEnvCSV("API_KEYS", ""),
		CORSAllowedOrigins: getEnvCSV("CORS_ALLOWED_ORIGINS", "*"),
		CORSAllowedMethods: getEnvCSV("CORS_ALLOWED_METHODS", "GET,POST,OPTIONS"),
		CORSAllowedHeaders: getEnvCSV("CORS_ALLOWED_HEADERS", "Content-Type,Authorization"),
		DockerMemoryLimit:    dockerMemLimit,
		DockerCPUPeriod:      dockerCPUPeriod,
		DockerCPUQuota:       dockerCPUQuota,
		DockerPidsLimit:      dockerPidsLimit,
		DockerReadonlyRootfs: getEnvBool("DOCKER_READONLY_ROOTFS", false),
		DockerTmpfsSizeMB:    dockerTmpfsSizeMB,
		MaxOutputBytes:       maxOutputBytes,
		DockerRuntime:        getEnv("DOCKER_RUNTIME", ""),
		DatabaseURL:          getEnv("DATABASE_URL", ""),
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return cfg, nil
}

func (c *Config) Validate() error {
	if c.MaxWorkers <= 0 {
		return errors.New("MAX_WORKERS must be greater than 0")
	}
	if c.DockerTimeoutMS <= 0 {
		return errors.New("DOCKER_TIMEOUT_MS must be greater than 0")
	}
	if c.RedisURL == "" {
		return errors.New("REDIS_URL cannot be empty")
	}
	if c.LanguageConfig == "" {
		return errors.New("LANGUAGES_CONFIG cannot be empty")
	}
	if c.PlaygroundEnabled && c.PlaygroundDir == "" {
		return errors.New("PLAYGROUND_DIR cannot be empty when playground is enabled")
	}
	if c.RateLimitRPM < 0 {
		return errors.New("RATE_LIMIT_RPM cannot be negative")
	}
	if c.DockerPidsLimit <= 0 {
		return errors.New("DOCKER_PIDS_LIMIT must be greater than 0")
	}
	if c.MaxOutputBytes <= 0 {
		return errors.New("MAX_OUTPUT_BYTES must be greater than 0")
	}
	if c.DockerReadonlyRootfs && c.DockerTmpfsSizeMB <= 0 {
		return errors.New("DOCKER_TMPFS_SIZE_MB must be greater than 0 when DOCKER_READONLY_ROOTFS is enabled")
	}
	return nil
}

func getEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) (int, error) {
	valStr, exists := os.LookupEnv(key)
	if !exists {
		return defaultValue, nil
	}
	val, err := strconv.Atoi(valStr)
	if err != nil {
		return 0, err
	}
	return val, nil
}

func getEnvInt64(key string, defaultValue int64) (int64, error) {
	valStr, exists := os.LookupEnv(key)
	if !exists {
		return defaultValue, nil
	}
	val, err := strconv.ParseInt(valStr, 10, 64)
	if err != nil {
		return 0, err
	}
	return val, nil
}

func getEnvBool(key string, defaultValue bool) bool {
	valStr, exists := os.LookupEnv(key)
	if !exists {
		return defaultValue
	}
	val, err := strconv.ParseBool(valStr)
	if err != nil {
		return defaultValue
	}
	return val
}

func getEnvCSV(key string, defaultValue string) []string {
	valStr := getEnv(key, defaultValue)
	parts := strings.Split(valStr, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value != "" {
			values = append(values, value)
		}
	}
	return values
}

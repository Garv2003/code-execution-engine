package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	Port              string
	MaxWorkers        int
	DockerTimeoutMS   int
	RedisURL          string
	DockerMemoryLimit string
	DockerCPUPeriod   int64
	DockerCPUQuota    int64
}

func Load() (*Config, error) {
	port := getEnv("PORT", "8080")
	redisURL := getEnv("REDIS_URL", "redis://localhost:6379")
	dockerMemLimit := getEnv("DOCKER_MEMORY_LIMIT", "128m")

	maxWorkers, err := getEnvInt("MAX_WORKERS", 10)
	if err != nil {
		return nil, fmt.Errorf("invalid MAX_WORKERS: %w", err)
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

	cfg := &Config{
		Port:              port,
		MaxWorkers:        maxWorkers,
		DockerTimeoutMS:   dockerTimeout,
		RedisURL:          redisURL,
		DockerMemoryLimit: dockerMemLimit,
		DockerCPUPeriod:   dockerCPUPeriod,
		DockerCPUQuota:    dockerCPUQuota,
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return cfg, nil
}

// Validate ensures the parsed configuration is valid.
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

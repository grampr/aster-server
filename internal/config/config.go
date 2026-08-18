package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddress     string
	DatabaseURL     string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	ShutdownTimeout time.Duration
	AutoMigrate     bool
}

func Load() (Config, error) {
	config := Config{
		HTTPAddress:     envOrDefault("ASTER_HTTP_ADDR", ":8080"),
		DatabaseURL:     os.Getenv("ASTER_DATABASE_URL"),
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 30 * 24 * time.Hour,
		ShutdownTimeout: 10 * time.Second,
	}
	if config.DatabaseURL == "" {
		return Config{}, errors.New("ASTER_DATABASE_URL is required")
	}

	var err error
	if config.AccessTokenTTL, err = durationFromEnv("ASTER_ACCESS_TOKEN_TTL", config.AccessTokenTTL); err != nil {
		return Config{}, err
	}
	if config.RefreshTokenTTL, err = durationFromEnv("ASTER_REFRESH_TOKEN_TTL", config.RefreshTokenTTL); err != nil {
		return Config{}, err
	}
	if config.ShutdownTimeout, err = durationFromEnv("ASTER_SHUTDOWN_TIMEOUT", config.ShutdownTimeout); err != nil {
		return Config{}, err
	}
	if config.AutoMigrate, err = boolFromEnv("ASTER_AUTO_MIGRATE", false); err != nil {
		return Config{}, err
	}
	if config.RefreshTokenTTL <= config.AccessTokenTTL {
		return Config{}, errors.New("ASTER_REFRESH_TOKEN_TTL must be greater than ASTER_ACCESS_TOKEN_TTL")
	}
	return config, nil
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func durationFromEnv(key string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s must be a positive Go duration", key)
	}
	return duration, nil
}

func boolFromEnv(key string, fallback bool) (bool, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", key)
	}
	return parsed, nil
}

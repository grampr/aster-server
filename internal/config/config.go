package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddress        string
	DatabaseURL        string
	AccessTokenTTL     time.Duration
	RefreshTokenTTL    time.Duration
	ShutdownTimeout    time.Duration
	AutoMigrate        bool
	GoogleClientID     string
	GoogleClientSecret string
	GoogleCallbackURL  string
	GoogleAttemptTTL   time.Duration
	GoogleExchangeTTL  time.Duration
}

func (c Config) GoogleEnabled() bool {
	return c.GoogleClientID != ""
}

func Load() (Config, error) {
	config := Config{
		HTTPAddress:        envOrDefault("ASTER_HTTP_ADDR", ":8080"),
		DatabaseURL:        os.Getenv("ASTER_DATABASE_URL"),
		AccessTokenTTL:     15 * time.Minute,
		RefreshTokenTTL:    30 * 24 * time.Hour,
		ShutdownTimeout:    10 * time.Second,
		GoogleClientID:     os.Getenv("ASTER_GOOGLE_CLIENT_ID"),
		GoogleClientSecret: os.Getenv("ASTER_GOOGLE_CLIENT_SECRET"),
		GoogleCallbackURL:  os.Getenv("ASTER_GOOGLE_CALLBACK_URL"),
		GoogleAttemptTTL:   5 * time.Minute,
		GoogleExchangeTTL:  2 * time.Minute,
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
	if config.GoogleAttemptTTL, err = durationFromEnv("ASTER_GOOGLE_ATTEMPT_TTL", config.GoogleAttemptTTL); err != nil {
		return Config{}, err
	}
	if config.GoogleExchangeTTL, err = durationFromEnv("ASTER_GOOGLE_EXCHANGE_TTL", config.GoogleExchangeTTL); err != nil {
		return Config{}, err
	}
	if config.RefreshTokenTTL <= config.AccessTokenTTL {
		return Config{}, errors.New("ASTER_REFRESH_TOKEN_TTL must be greater than ASTER_ACCESS_TOKEN_TTL")
	}
	googleValues := 0
	for _, value := range []string{config.GoogleClientID, config.GoogleClientSecret, config.GoogleCallbackURL} {
		if value != "" {
			googleValues++
		}
	}
	if googleValues != 0 && googleValues != 3 {
		return Config{}, errors.New("ASTER_GOOGLE_CLIENT_ID, ASTER_GOOGLE_CLIENT_SECRET, and ASTER_GOOGLE_CALLBACK_URL must be set together")
	}
	if config.GoogleEnabled() {
		callback, parseErr := url.Parse(config.GoogleCallbackURL)
		if parseErr != nil || callback.Scheme == "" || callback.Host == "" {
			return Config{}, errors.New("ASTER_GOOGLE_CALLBACK_URL must be an absolute URL")
		}
		if callback.Scheme != "https" && !(callback.Scheme == "http" && (callback.Hostname() == "localhost" || callback.Hostname() == "127.0.0.1")) {
			return Config{}, errors.New("ASTER_GOOGLE_CALLBACK_URL must use HTTPS except on localhost")
		}
	}
	if config.GoogleAttemptTTL > 10*time.Minute || config.GoogleExchangeTTL > 10*time.Minute {
		return Config{}, errors.New("Google authentication TTLs must not exceed 10 minutes")
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

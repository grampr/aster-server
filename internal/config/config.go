package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddress             string
	DatabaseURL             string
	AccessTokenTTL          time.Duration
	RefreshTokenTTL         time.Duration
	ShutdownTimeout         time.Duration
	GatewayURL              string
	GatewayHeartbeat        time.Duration
	GatewayIdentifyTimeout  time.Duration
	GatewaySessionRetention time.Duration
	GatewayAllowedOrigins   []string
	AutoMigrate             bool
	Storage                 StorageConfig
	Voice                   VoiceConfig
	Google                  GoogleConfig
	SMTP                    SMTPConfig
}

// SMTPConfig describes the mail server for verification and password reset email.
// Those flows are disabled when Addr is empty.
type SMTPConfig struct {
	Addr     string
	Username string
	Password string
	From     string
	// TLS is "starttls" (default), "tls" or "none".
	TLS string
}

func (c SMTPConfig) Enabled() bool { return c.Addr != "" }

// GoogleConfig describes the Google OAuth client. Google Login is disabled when
// ClientID is empty.
type GoogleConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

func (c GoogleConfig) Enabled() bool { return c.ClientID != "" }

// VoiceConfig describes the LiveKit server used for Voice Channels.
// Voice is disabled when URL is empty.
type VoiceConfig struct {
	URL       string
	APIURL    string
	APIKey    string
	APISecret string
}

func (c VoiceConfig) Enabled() bool { return c.URL != "" }

// StorageConfig describes the S3-compatible Object Storage for attachments.
// Attachments are disabled when Endpoint is empty.
type StorageConfig struct {
	Endpoint       string
	PublicEndpoint string
	Region         string
	Bucket         string
	AccessKey      string
	SecretKey      string
	PathStyle      bool
}

func (c StorageConfig) Enabled() bool { return c.Endpoint != "" }

func Load() (Config, error) {
	config := Config{
		HTTPAddress:             envOrDefault("ASTER_HTTP_ADDR", ":8080"),
		DatabaseURL:             os.Getenv("ASTER_DATABASE_URL"),
		AccessTokenTTL:          15 * time.Minute,
		RefreshTokenTTL:         30 * 24 * time.Hour,
		ShutdownTimeout:         10 * time.Second,
		GatewayURL:              envOrDefault("ASTER_GATEWAY_URL", "ws://localhost:8080/gateway/v1"),
		GatewayHeartbeat:        45 * time.Second,
		GatewayIdentifyTimeout:  10 * time.Second,
		GatewaySessionRetention: 2 * time.Minute,
		GatewayAllowedOrigins:   splitCSV(envOrDefault("ASTER_GATEWAY_ALLOWED_ORIGINS", "http://localhost:5173,http://127.0.0.1:5173,tauri://localhost,http://tauri.localhost")),
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
	if config.GatewayHeartbeat, err = durationFromEnv("ASTER_GATEWAY_HEARTBEAT_INTERVAL", config.GatewayHeartbeat); err != nil {
		return Config{}, err
	}
	if config.GatewayIdentifyTimeout, err = durationFromEnv("ASTER_GATEWAY_IDENTIFY_TIMEOUT", config.GatewayIdentifyTimeout); err != nil {
		return Config{}, err
	}
	if config.GatewaySessionRetention, err = durationFromEnv("ASTER_GATEWAY_SESSION_RETENTION", config.GatewaySessionRetention); err != nil {
		return Config{}, err
	}
	if config.AutoMigrate, err = boolFromEnv("ASTER_AUTO_MIGRATE", false); err != nil {
		return Config{}, err
	}
	config.Storage = StorageConfig{
		Endpoint:       os.Getenv("ASTER_STORAGE_ENDPOINT"),
		PublicEndpoint: os.Getenv("ASTER_STORAGE_PUBLIC_ENDPOINT"),
		Region:         envOrDefault("ASTER_STORAGE_REGION", "us-east-1"),
		Bucket:         os.Getenv("ASTER_STORAGE_BUCKET"),
		AccessKey:      os.Getenv("ASTER_STORAGE_ACCESS_KEY"),
		SecretKey:      os.Getenv("ASTER_STORAGE_SECRET_KEY"),
	}
	if config.Storage.PathStyle, err = boolFromEnv("ASTER_STORAGE_PATH_STYLE", true); err != nil {
		return Config{}, err
	}
	if config.Storage.Enabled() && (config.Storage.Bucket == "" || config.Storage.AccessKey == "" || config.Storage.SecretKey == "") {
		return Config{}, errors.New("ASTER_STORAGE_BUCKET, ASTER_STORAGE_ACCESS_KEY and ASTER_STORAGE_SECRET_KEY are required when ASTER_STORAGE_ENDPOINT is set")
	}
	config.Voice = VoiceConfig{
		URL:       os.Getenv("ASTER_VOICE_LIVEKIT_URL"),
		APIURL:    os.Getenv("ASTER_VOICE_LIVEKIT_API_URL"),
		APIKey:    os.Getenv("ASTER_VOICE_LIVEKIT_API_KEY"),
		APISecret: os.Getenv("ASTER_VOICE_LIVEKIT_API_SECRET"),
	}
	if config.Voice.Enabled() && (config.Voice.APIKey == "" || config.Voice.APISecret == "") {
		return Config{}, errors.New("ASTER_VOICE_LIVEKIT_API_KEY and ASTER_VOICE_LIVEKIT_API_SECRET are required when ASTER_VOICE_LIVEKIT_URL is set")
	}
	config.Google = GoogleConfig{
		ClientID:     os.Getenv("ASTER_GOOGLE_CLIENT_ID"),
		ClientSecret: os.Getenv("ASTER_GOOGLE_CLIENT_SECRET"),
		RedirectURL:  os.Getenv("ASTER_GOOGLE_REDIRECT_URL"),
	}
	if config.Google.Enabled() && (config.Google.ClientSecret == "" || config.Google.RedirectURL == "") {
		return Config{}, errors.New("ASTER_GOOGLE_CLIENT_SECRET and ASTER_GOOGLE_REDIRECT_URL are required when ASTER_GOOGLE_CLIENT_ID is set")
	}
	config.SMTP = SMTPConfig{
		Addr:     os.Getenv("ASTER_SMTP_ADDR"),
		Username: os.Getenv("ASTER_SMTP_USERNAME"),
		Password: os.Getenv("ASTER_SMTP_PASSWORD"),
		From:     os.Getenv("ASTER_SMTP_FROM"),
		TLS:      envOrDefault("ASTER_SMTP_TLS", "starttls"),
	}
	if config.SMTP.Enabled() && config.SMTP.From == "" {
		return Config{}, errors.New("ASTER_SMTP_FROM is required when ASTER_SMTP_ADDR is set")
	}
	if config.RefreshTokenTTL <= config.AccessTokenTTL {
		return Config{}, errors.New("ASTER_REFRESH_TOKEN_TTL must be greater than ASTER_ACCESS_TOKEN_TTL")
	}
	return config, nil
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			values = append(values, value)
		}
	}
	return values
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

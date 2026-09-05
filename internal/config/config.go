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
	HTTPAllowedOrigins      []string
	AutoMigrate             bool
	ObjectStorageEndpoint   string
	ObjectStorageRegion     string
	ObjectStorageBucket     string
	ObjectStorageAccessKey  string
	ObjectStorageSecretKey  string
	ObjectStoragePathStyle  bool
	VoiceProvider           string
	RealtimeAccountID       string
	RealtimeAppID           string
	RealtimeAPIToken        string
	RealtimeListenerPreset  string
	RealtimeVoicePreset     string
	RealtimeStreamPreset    string
}

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
		HTTPAllowedOrigins:      splitCSV(envOrDefault("ASTER_HTTP_ALLOWED_ORIGINS", "http://localhost:5173,http://127.0.0.1:5173,tauri://localhost,http://tauri.localhost")),
		ObjectStorageEndpoint:   os.Getenv("ASTER_OBJECT_STORAGE_ENDPOINT"), ObjectStorageRegion: envOrDefault("ASTER_OBJECT_STORAGE_REGION", "auto"), ObjectStorageBucket: os.Getenv("ASTER_OBJECT_STORAGE_BUCKET"), ObjectStorageAccessKey: os.Getenv("ASTER_OBJECT_STORAGE_ACCESS_KEY_ID"), ObjectStorageSecretKey: os.Getenv("ASTER_OBJECT_STORAGE_SECRET_ACCESS_KEY"),
		VoiceProvider:     strings.ToLower(strings.TrimSpace(os.Getenv("ASTER_VOICE_PROVIDER"))),
		RealtimeAccountID: os.Getenv("ASTER_CLOUDFLARE_ACCOUNT_ID"), RealtimeAppID: os.Getenv("ASTER_CLOUDFLARE_REALTIME_APP_ID"), RealtimeAPIToken: os.Getenv("ASTER_CLOUDFLARE_API_TOKEN"), RealtimeListenerPreset: envOrDefault("ASTER_CLOUDFLARE_REALTIME_LISTENER_PRESET", "group_call_listener"), RealtimeVoicePreset: envOrDefault("ASTER_CLOUDFLARE_REALTIME_VOICE_PRESET", "group_call_participant"), RealtimeStreamPreset: envOrDefault("ASTER_CLOUDFLARE_REALTIME_STREAM_PRESET", "group_call_host"),
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
	if config.ObjectStoragePathStyle, err = boolFromEnv("ASTER_OBJECT_STORAGE_PATH_STYLE", false); err != nil {
		return Config{}, err
	}
	storageValues := []string{config.ObjectStorageEndpoint, config.ObjectStorageBucket, config.ObjectStorageAccessKey, config.ObjectStorageSecretKey}
	if anySet(storageValues) && !allSet(storageValues) {
		return Config{}, errors.New("all ASTER_OBJECT_STORAGE_* endpoint, bucket, and credential values are required together")
	}
	realtimeValues := []string{config.RealtimeAccountID, config.RealtimeAppID, config.RealtimeAPIToken}
	if anySet(realtimeValues) && !allSet(realtimeValues) {
		return Config{}, errors.New("Cloudflare Realtime account, app, and API token values are required together")
	}
	if config.VoiceProvider == "" && allSet(realtimeValues) {
		config.VoiceProvider = "cloudflare-realtimekit"
	}
	if config.VoiceProvider != "" && config.VoiceProvider != "cloudflare-realtimekit" && config.VoiceProvider != "aster-local" {
		return Config{}, errors.New("ASTER_VOICE_PROVIDER must be cloudflare-realtimekit, aster-local, or empty")
	}
	if config.VoiceProvider == "cloudflare-realtimekit" && !allSet(realtimeValues) {
		return Config{}, errors.New("Cloudflare Realtime credentials are required for ASTER_VOICE_PROVIDER=cloudflare-realtimekit")
	}
	if config.RefreshTokenTTL <= config.AccessTokenTTL {
		return Config{}, errors.New("ASTER_REFRESH_TOKEN_TTL must be greater than ASTER_ACCESS_TOKEN_TTL")
	}
	return config, nil
}

func anySet(values []string) bool {
	for _, value := range values {
		if value != "" {
			return true
		}
	}
	return false
}
func allSet(values []string) bool {
	for _, value := range values {
		if value == "" {
			return false
		}
	}
	return true
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

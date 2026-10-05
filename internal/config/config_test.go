package config

import (
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	t.Setenv("ASTER_DATABASE_URL", "postgres://example")
	t.Setenv("ASTER_ACCESS_TOKEN_TTL", "5m")
	t.Setenv("ASTER_REFRESH_TOKEN_TTL", "24h")
	t.Setenv("ASTER_AUTO_MIGRATE", "true")

	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.HTTPAddress != ":8080" || !config.AutoMigrate {
		t.Fatalf("unexpected config: %+v", config)
	}
	if config.GatewayURL != "ws://localhost:8080/gateway/v1" || config.GatewayHeartbeat != 45*time.Second {
		t.Fatalf("unexpected gateway config: %+v", config)
	}
	if len(config.GatewayAllowedOrigins) != 4 {
		t.Fatalf("unexpected gateway origins: %v", config.GatewayAllowedOrigins)
	}
}

func TestLoadParsesGatewaySettings(t *testing.T) {
	t.Setenv("ASTER_DATABASE_URL", "postgres://example")
	t.Setenv("ASTER_GATEWAY_HEARTBEAT_INTERVAL", "30s")
	t.Setenv("ASTER_GATEWAY_ALLOWED_ORIGINS", "https://aster.example, tauri://localhost")
	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.GatewayHeartbeat != 30*time.Second || len(config.GatewayAllowedOrigins) != 2 {
		t.Fatalf("unexpected gateway config: %+v", config)
	}
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	t.Setenv("ASTER_DATABASE_URL", "")
	if _, err := Load(); err == nil {
		t.Fatal("missing database URL must be rejected")
	}
}

func TestLoadRejectsRefreshTTLShorterThanAccessTTL(t *testing.T) {
	t.Setenv("ASTER_DATABASE_URL", "postgres://example")
	t.Setenv("ASTER_ACCESS_TOKEN_TTL", "2h")
	t.Setenv("ASTER_REFRESH_TOKEN_TTL", "1h")
	if _, err := Load(); err == nil {
		t.Fatal("refresh TTL shorter than access TTL must be rejected")
	}
}

func TestLoadStorageIsOptionalButAllOrNothing(t *testing.T) {
	t.Setenv("ASTER_DATABASE_URL", "postgres://example")
	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.Storage.Enabled() {
		t.Fatal("storage must be disabled without an endpoint")
	}

	t.Setenv("ASTER_STORAGE_ENDPOINT", "http://localhost:9000")
	if _, err := Load(); err == nil {
		t.Fatal("an endpoint without credentials and a bucket must be rejected")
	}
	t.Setenv("ASTER_STORAGE_BUCKET", "aster")
	t.Setenv("ASTER_STORAGE_ACCESS_KEY", "key")
	t.Setenv("ASTER_STORAGE_SECRET_KEY", "secret")
	config, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if !config.Storage.Enabled() || config.Storage.Region != "us-east-1" || !config.Storage.PathStyle {
		t.Fatalf("unexpected storage config: %+v", config.Storage)
	}
}

func TestLoadVoiceIsOptionalButAllOrNothing(t *testing.T) {
	t.Setenv("ASTER_DATABASE_URL", "postgres://example")
	config, err := Load()
	if err != nil || config.Voice.Enabled() {
		t.Fatalf("voice must be disabled without a URL: %v %+v", err, config.Voice)
	}
	t.Setenv("ASTER_VOICE_LIVEKIT_URL", "ws://localhost:7880")
	if _, err := Load(); err == nil {
		t.Fatal("a URL without API credentials must be rejected")
	}
	t.Setenv("ASTER_VOICE_LIVEKIT_API_KEY", "key")
	t.Setenv("ASTER_VOICE_LIVEKIT_API_SECRET", "secret")
	if config, err = Load(); err != nil || !config.Voice.Enabled() {
		t.Fatalf("unexpected voice config: %v %+v", err, config.Voice)
	}
}

func TestLoadGoogleAndSMTPAreOptionalButAllOrNothing(t *testing.T) {
	t.Setenv("ASTER_DATABASE_URL", "postgres://example")
	config, err := Load()
	if err != nil || config.Google.Enabled() || config.SMTP.Enabled() {
		t.Fatalf("both must be disabled by default: %v %+v %+v", err, config.Google, config.SMTP)
	}

	t.Setenv("ASTER_GOOGLE_CLIENT_ID", "id")
	if _, err := Load(); err == nil {
		t.Fatal("a Google client ID without a secret and redirect URL must be rejected")
	}
	t.Setenv("ASTER_GOOGLE_CLIENT_SECRET", "secret")
	t.Setenv("ASTER_GOOGLE_REDIRECT_URL", "https://aster.example/api/v1/auth/google/callback")
	if config, err = Load(); err != nil || !config.Google.Enabled() {
		t.Fatalf("unexpected google config: %v %+v", err, config.Google)
	}

	t.Setenv("ASTER_SMTP_ADDR", "localhost:25")
	if _, err := Load(); err == nil {
		t.Fatal("an SMTP address without a sender must be rejected")
	}
	t.Setenv("ASTER_SMTP_FROM", "Aster <no-reply@example.com>")
	if config, err = Load(); err != nil || !config.SMTP.Enabled() || config.SMTP.TLS != "starttls" {
		t.Fatalf("unexpected smtp config: %v %+v", err, config.SMTP)
	}
}

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
	if len(config.HTTPAllowedOrigins) != 4 {
		t.Fatalf("unexpected HTTP origins: %v", config.HTTPAllowedOrigins)
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

func TestLoadAllowsExplicitLocalVoiceProvider(t *testing.T) {
	t.Setenv("ASTER_DATABASE_URL", "postgres://example")
	t.Setenv("ASTER_VOICE_PROVIDER", "aster-local")
	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.VoiceProvider != "aster-local" {
		t.Fatalf("unexpected voice provider: %q", config.VoiceProvider)
	}
}

func TestLoadRejectsCloudflareProviderWithoutCredentials(t *testing.T) {
	t.Setenv("ASTER_DATABASE_URL", "postgres://example")
	t.Setenv("ASTER_VOICE_PROVIDER", "cloudflare-realtimekit")
	if _, err := Load(); err == nil {
		t.Fatal("missing Cloudflare credentials must be rejected")
	}
}

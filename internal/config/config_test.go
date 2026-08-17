package config

import "testing"

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

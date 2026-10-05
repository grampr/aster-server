package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/grampr/aster-server/internal/auth"
	"github.com/grampr/aster-server/internal/chat"
	"github.com/grampr/aster-server/internal/config"
	"github.com/grampr/aster-server/internal/gateway"
	"github.com/grampr/aster-server/internal/httpapi"
	"github.com/grampr/aster-server/internal/media"
	postgresplatform "github.com/grampr/aster-server/internal/platform/postgres"
	"github.com/grampr/aster-server/migrations"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	config, err := config.Load()
	if err != nil {
		return err
	}

	rootContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startupContext, cancelStartup := context.WithTimeout(rootContext, 10*time.Second)
	defer cancelStartup()

	pool, err := postgresplatform.Open(startupContext, config.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if config.AutoMigrate {
		if err := postgresplatform.Migrate(startupContext, pool, migrations.FS); err != nil {
			return err
		}
	}

	hasher, err := auth.NewPasswordHasher(auth.DefaultPasswordParams())
	if err != nil {
		return err
	}
	authService, err := auth.NewService(
		auth.NewPostgresStore(pool), hasher,
		config.AccessTokenTTL, config.RefreshTokenTTL,
	)
	if err != nil {
		return err
	}
	chatStore := chat.NewPostgresStore(pool)
	chatService, err := chat.NewService(chatStore)
	if err != nil {
		return err
	}
	if config.Storage.Enabled() {
		storage, err := media.NewS3Storage(media.S3Config{
			Endpoint: config.Storage.Endpoint, PublicEndpoint: config.Storage.PublicEndpoint, Region: config.Storage.Region,
			Bucket: config.Storage.Bucket, AccessKey: config.Storage.AccessKey, SecretKey: config.Storage.SecretKey,
			PathStyle: config.Storage.PathStyle,
		})
		if err != nil {
			return err
		}
		chatService.WithStorage(storage)
		go media.NewJanitor(chatStore, storage, logger).Run(rootContext, time.Minute)
	} else {
		logger.Warn("ASTER_STORAGE_ENDPOINT is not set; attachments are disabled")
	}
	gatewayService, err := gateway.New(authService, gateway.Config{
		URL: config.GatewayURL, HeartbeatInterval: config.GatewayHeartbeat,
		IdentifyTimeout: config.GatewayIdentifyTimeout, SessionRetention: config.GatewaySessionRetention,
		EventBufferSize: 1000, AllowedOrigins: config.GatewayAllowedOrigins,
	}, logger)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr:              config.HTTPAddress,
		Handler:           httpapi.New(authService, chatService, gatewayService, logger, version),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverError := make(chan error, 1)
	go func() {
		logger.Info("HTTP server listening", "address", config.HTTPAddress, "version", version)
		serverError <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-serverError:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-rootContext.Done():
	}

	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), config.ShutdownTimeout)
	defer cancelShutdown()
	if err := httpServer.Shutdown(shutdownContext); err != nil {
		return err
	}
	if err := <-serverError; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

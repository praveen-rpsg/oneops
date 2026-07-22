// Command controlplane is the OneOps Configuration Registry HTTP service.
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

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/httpapi"
	"github.com/rpsg/oneops/internal/observability"
	"github.com/rpsg/oneops/internal/store/migrate"
	"github.com/rpsg/oneops/internal/store/postgres"
	"github.com/rpsg/oneops/pkg/version"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	rootCtx := context.Background()

	shutdownTracing, err := observability.SetupTracing(rootCtx, cfg.ServiceName, cfg.OTLPEndpoint)
	if err != nil {
		return err
	}

	pool, err := postgres.NewPool(rootCtx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := postgres.WaitForDB(rootCtx, pool, 30*time.Second); err != nil {
		return err
	}

	if cfg.AutoMigrate {
		migCtx, cancel := context.WithTimeout(rootCtx, 30*time.Second)
		err := migrate.Up(migCtx, pool)
		cancel()
		if err != nil {
			return err
		}
	}

	repo := postgres.NewConfigObjectRepo(pool)
	idem := postgres.NewIdempotencyStore(pool)
	verifier := auth.NewVerifier(cfg.JWTIssuer, cfg.JWTAudience, cfg.JWTHMACKey, cfg.JWKSURL)
	metrics := observability.NewMetrics()

	srv := httpapi.NewServer(cfg, logger, repo, idem, verifier, metrics, pool.Ping)
	httpServer := srv.HTTPServer()

	logger.Info("starting oneops control plane",
		"version", version.Version, "commit", version.Commit,
		"addr", cfg.HTTPAddr, "env", cfg.Env)

	ctx, stop := signal.NotifyContext(rootCtx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown signal received, draining")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.ShutdownGrace)*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := shutdownTracing(shutdownCtx); err != nil {
		logger.Warn("tracing shutdown", "err", err)
	}
	logger.Info("shutdown complete")
	return nil
}

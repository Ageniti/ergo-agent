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

	"github.com/ageniti/ergo-agent/examples/ecs/internal/config"
	"github.com/ageniti/ergo-agent/examples/ecs/internal/httpapi"
	mysqlrepo "github.com/ageniti/ergo-agent/examples/ecs/internal/repository/mysql"
	"github.com/ageniti/ergo-agent/examples/ecs/internal/service"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration error", "error", err)
		os.Exit(1)
	}
	if err := cfg.ValidateAPI(); err != nil {
		logger.Error("API configuration error", "error", err)
		os.Exit(1)
	}
	repo, err := mysqlrepo.OpenWithOptions(cfg.MySQLDSN, mysqlrepo.Options{MaxOpen: cfg.DBMaxOpen, MaxIdle: cfg.DBMaxIdle, MaxLifetime: cfg.DBConnMaxLifetime})
	if err != nil {
		logger.Error("database open failed", "error", err)
		os.Exit(1)
	}
	defer repo.Close()
	pingCtx, cancelPing := context.WithTimeout(context.Background(), 10*time.Second)
	if err := repo.Ping(pingCtx); err != nil {
		cancelPing()
		logger.Error("database ping failed", "error", err)
		os.Exit(1)
	}
	cancelPing()

	server := &http.Server{
		Addr:              cfg.HTTPAddress,
		Handler:           httpapi.NewServer(service.NewRunService(repo), repo, logger, cfg.InternalToken).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	serveErrors := make(chan error, 1)
	go func() {
		logger.Info("agent api listening", "address", cfg.HTTPAddress)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErrors <- err
		}
	}()
	select {
	case err := <-serveErrors:
		logger.Error("http server failed", "error", err)
		_ = repo.Close()
		os.Exit(1)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	}
}

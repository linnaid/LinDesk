package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"lindesk/internal/auth"
	"lindesk/internal/config"
	"lindesk/internal/database"
	"lindesk/internal/httpapi"
	"lindesk/internal/refund"
)

const version = "dev"

func main() {
	configPath := flag.String("config", "", "path to an optional JSON configuration file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load configuration: %v\n", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{}))
	databaseContext, cancelDatabaseContext := context.WithTimeout(context.Background(), 3*time.Second)
	databaseHandle, err := database.Open(databaseContext, cfg.Database)
	cancelDatabaseContext()
	if err != nil {
		if database.IsDisabled(err) {
			logger.Info("Database connection disabled; using in-memory repositories", "driver", cfg.Database.Driver)
		} else {
			logger.Warn("Database connection unavailable; using in-memory repositories", "driver", cfg.Database.Driver, "error", err)
		}
	} else {
		defer databaseHandle.Close()
		logger.Info("Database connection ready", "driver", cfg.Database.Driver)
	}

	var refundRepository refund.Repository
	if databaseHandle != nil {
		refundRepository = refund.NewPostgresRepository(databaseHandle)
		logger.Info("Using PostgreSQL refund repository")
	} else {
		refundRepository = refund.NewInMemoryRepository(refund.DemoOrders())
		logger.Info("Using in-memory refund repository")
	}

	refundService := refund.NewService(
		refundRepository,
		cfg.Refund.HighAmountApprovalThreshold,
		refund.SystemClock{},
		refund.NewSequentialRequestNumberGenerator(),
	)
	var authService auth.Authenticator
	if databaseHandle != nil {
		authService = auth.NewPostgresService(databaseHandle)
		logger.Info("Using PostgreSQL auth service")
	} else {
		authService = auth.NewDemoService()
		logger.Info("Using in-memory auth service")
	}

	server := httpapi.NewServer(cfg.Service.HTTPAddr, cfg.Service.Name, version, httpapi.Dependencies{
		Refunds: refundService,
		Auth:    authService,
	})
	shutdownTimeout := cfg.Service.ShutdownTimeout.Value()

	go func() {
		logger.Info(
			"LinDesk server starting",
			"address", cfg.Service.HTTPAddr,
			"environment", cfg.Service.Environment,
			"version", version,
		)

		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("LinDesk server stopped unexpectedly", "error", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownContext); err != nil {
		logger.Error("LinDesk graceful shutdown failed", "error", err)
		return
	}

	logger.Info("LinDesk server stopped", "shutdown_timeout", shutdownTimeout.Round(time.Second).String())
}

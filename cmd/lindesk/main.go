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
	refundRepository := refund.NewInMemoryRepository(refund.DemoOrders())
	refundService := refund.NewService(
		refundRepository,
		cfg.Refund.HighAmountApprovalThreshold,
		refund.SystemClock{},
		refund.NewSequentialRequestNumberGenerator(),
	)
	authService := auth.NewDemoService()
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

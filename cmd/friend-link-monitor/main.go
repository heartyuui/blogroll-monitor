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

	"github.com/heartyuui/blogroll-monitor/internal/checker"
	"github.com/heartyuui/blogroll-monitor/internal/config"
	"github.com/heartyuui/blogroll-monitor/internal/health"
	"github.com/heartyuui/blogroll-monitor/internal/monitor"
	"github.com/heartyuui/blogroll-monitor/internal/signing"
	"github.com/heartyuui/blogroll-monitor/internal/store"
	"github.com/heartyuui/blogroll-monitor/internal/syncclient"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, attribute slog.Attr) slog.Attr {
			if attribute.Key == slog.TimeKey {
				attribute.Value = slog.TimeValue(attribute.Value.Time().UTC())
			}
			return attribute
		},
	}))
	slog.SetDefault(logger)

	configuration, err := config.Load()
	if err != nil {
		logger.Error("configuration is invalid", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	database, err := store.Open(ctx, configuration.DatabasePath)
	if err != nil {
		logger.Error("database initialization failed", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	blogClient, err := syncclient.New(configuration.BlogBaseURL, configuration.NodeID, signing.Signer{
		KeyID:  configuration.HMACKeyID,
		Secret: configuration.HMACSecret,
	}, configuration.BlogAllowInsecureLocal)
	if err != nil {
		logger.Error("blog client initialization failed", "error", err)
		os.Exit(1)
	}
	defer blogClient.Close()

	targetChecker := checker.New(checker.Options{
		Policy: checker.Policy{
			LookupTimeout:     configuration.DNSLookupTimeout,
			AllowTestLoopback: configuration.TestAllowLoopback,
		},
		ConnectTimeout:        configuration.ConnectTimeout,
		TLSHandshakeTimeout:   configuration.TLSHandshakeTimeout,
		ResponseHeaderTimeout: configuration.ResponseHeaderTimeout,
		ReadTimeout:           configuration.ReadTimeout,
		TotalTimeout:          configuration.TotalTimeout,
		MaxResponseBytes:      configuration.MaxResponseBytes,
		MaxRedirects:          configuration.MaxRedirects,
		Retries:               configuration.TargetRetries,
		AllowHTTPSDowngrade:   configuration.AllowHTTPSDowngrade,
		UserAgent:             "blogroll-monitor/1",
	})
	defer targetChecker.Close()

	service := monitor.New(configuration, database, targetChecker, blogClient, logger)
	server := &http.Server{
		Addr:              configuration.ListenAddress,
		Handler:           health.Handler(database, service),
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	go service.Run(ctx)
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("health server started", "address", configuration.ListenAddress)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("health server stopped unexpectedly", "error", err)
			stop()
		}
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		logger.Error("health server shutdown failed", "error", err)
	}
	logger.Info("service stopped")
}

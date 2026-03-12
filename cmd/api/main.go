package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/urlshortener/platform/internal/config"
	"github.com/urlshortener/platform/pkg/logger"
	"github.com/urlshortener/platform/pkg/telemetry"
)

// Build-time variables injected via ldflags.
// These are set by the Makefile: -X main.version=...
var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

func main() {
	// -------------------------------------------------------
	// 1. Load Configuration
	// All config is read from environment variables per 12-Factor.
	// Fail fast on missing required variables.
	// -------------------------------------------------------
	cfg, err := config.Load()
	if err != nil {
		// Use default stderr before logger is initialized
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	// -------------------------------------------------------
	// 2. Initialize Structured Logger
	// JSON format in production, text for local development.
	// slog is stdlib since Go 1.21 — zero external dependency.
	// -------------------------------------------------------
	log := logger.New(cfg.LogLevel, cfg.LogFormat).With(
		slog.String("service", cfg.ServiceName),
		slog.String("version", version),
		slog.String("commit", commit),
		slog.String("env", cfg.Environment),
	)

	log.Info("starting service",
		slog.String("build_time", buildTime),
		slog.String("port", cfg.APIPort),
	)

	// -------------------------------------------------------
	// 3. Initialize OpenTelemetry
	// Tracing is initialized before any I/O so that startup
	// operations are also traced. Shutdown is deferred so all
	// spans are flushed even on graceful shutdown.
	// -------------------------------------------------------
	ctx := context.Background()
	otelShutdown, err := telemetry.InitTracer(ctx, telemetry.Config{
		Enabled:        cfg.OTelEnabled,
		Exporter:       cfg.OTelExporter,
		OTLPEndpoint:   cfg.OTelEndpoint,
		ServiceName:    cfg.ServiceName,
		ServiceVersion: version,
		Environment:    cfg.Environment,
		SampleRate:     cfg.OTelSampleRate,
	})
	if err != nil {
		log.Error("failed to initialize opentelemetry", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// -------------------------------------------------------
	// 4. Build HTTP Router
	// Chi is chosen per PRD dependency spec. It is lightweight,
	// idiomatic, and supports per-route middleware composition
	// which we need for auth, rate limiting, and tracing.
	// -------------------------------------------------------
	r := chi.NewRouter()

	// Core middleware applied to every request.
	// Order matters: RequestID must come before Logger so the
	// logger can read the request ID from the context.
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(time.Duration(cfg.APIWriteTimeoutS) * time.Second))

	// Health endpoints — no auth, no middleware, fast path.
	// /healthz: liveness probe  — process is alive
	// /readyz:  readiness probe — dependencies are healthy
	r.Get("/healthz", handleLiveness())
	r.Get("/readyz", handleReadiness(log))

	// API routes will be registered here in subsequent stories.
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/ping", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok","service":"api-service"}`))
		})
	})

	// -------------------------------------------------------
	// 5. Configure HTTP Server
	// Timeouts are explicitly set to prevent Slowloris attacks
	// and resource exhaustion. These values are configurable
	// via environment so we can tune per environment.
	// -------------------------------------------------------
	srv := &http.Server{
		Addr:         ":" + cfg.APIPort,
		Handler:      r,
		ReadTimeout:  time.Duration(cfg.APIReadTimeoutS) * time.Second,
		WriteTimeout: time.Duration(cfg.APIWriteTimeoutS) * time.Second,
		IdleTimeout:  time.Duration(cfg.APIIdleTimeoutS) * time.Second,
	}

	// -------------------------------------------------------
	// 6. Start Server (non-blocking)
	// Server runs in a goroutine so the main goroutine can
	// block on the signal channel for graceful shutdown.
	// -------------------------------------------------------
	serverErr := make(chan error, 1)
	go func() {
		log.Info("http server listening", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- fmt.Errorf("http server error: %w", err)
		}
	}()

	// -------------------------------------------------------
	// 7. Graceful Shutdown
	// We listen for SIGINT (Ctrl+C) and SIGTERM (Kubernetes
	// sends SIGTERM before SIGKILL after terminationGracePeriod).
	//
	// Kubernetes shutdown sequence:
	//   - Pod enters Terminating state
	//   - preStop hook runs (sleep 10s to drain load balancer)
	//   - SIGTERM sent to container
	//   - We have terminationGracePeriodSeconds to finish
	//
	// Our shutdown sequence:
	//   1. Stop accepting new connections (srv.Shutdown)
	//   2. Wait for in-flight requests to complete
	//   3. Flush OTel spans
	//   4. Close DB connections (added in Story 1.3)
	//   5. Close Redis connections (added in Story 1.4)
	// -------------------------------------------------------
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		log.Error("server error, initiating shutdown", slog.String("error", err.Error()))
	case sig := <-quit:
		log.Info("shutdown signal received", slog.String("signal", sig.String()))
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(
		context.Background(),
		time.Duration(cfg.APIShutdownTimeoutS)*time.Second,
	)
	defer shutdownCancel()

	log.Info("shutting down http server")
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("http server shutdown error", slog.String("error", err.Error()))
	}

	log.Info("flushing telemetry spans")
	if err := otelShutdown(shutdownCtx); err != nil {
		log.Error("otel shutdown error", slog.String("error", err.Error()))
	}

	log.Info("shutdown complete")
}

// handleLiveness returns the liveness probe handler.
// This endpoint only verifies the process is running — it performs
// no dependency checks. Kubernetes restarts the pod if this fails.
func handleLiveness() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"alive"}`))
	}
}

// handleReadiness returns the readiness probe handler.
// In Story 1.3+ this will check DB and Redis connectivity.
// Kubernetes stops sending traffic if this returns non-2xx,
// which is the correct behavior during startup or dependency outage.
func handleReadiness(log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// TODO(story-1.3): Add PostgreSQL ping check
		// TODO(story-1.4): Add Redis ping check
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	}
}
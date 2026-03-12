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
var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	// The redirect service uses a different service name for telemetry
	// so traces from both services are distinguishable in Jaeger/Tempo.
	log := logger.New(cfg.LogLevel, cfg.LogFormat).With(
		slog.String("service", "redirect-service"),
		slog.String("version", version),
		slog.String("commit", commit),
		slog.String("env", cfg.Environment),
	)

	log.Info("starting service",
		slog.String("build_time", buildTime),
		slog.String("port", cfg.RedirectPort),
	)

	ctx := context.Background()
	otelShutdown, err := telemetry.InitTracer(ctx, telemetry.Config{
		Enabled:        cfg.OTelEnabled,
		Exporter:       cfg.OTelExporter,
		OTLPEndpoint:   cfg.OTelEndpoint,
		ServiceName:    "redirect-service",
		ServiceVersion: version,
		Environment:    cfg.Environment,
		SampleRate:     cfg.OTelSampleRate,
	})
	if err != nil {
		log.Error("failed to initialize opentelemetry", slog.String("error", err.Error()))
		os.Exit(1)
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	// The redirect service has a tighter timeout than the API service.
	// Our SLO target is P99 < 50ms. The timeout provides a hard upper bound
	// that prevents slow requests from holding connections.
	r.Use(middleware.Timeout(time.Duration(cfg.RedirectWriteTimeoutS) * time.Second))

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"alive"}`))
	})

	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		// TODO(story-1.3): Check PostgreSQL connectivity
		// TODO(story-1.4): Check Redis connectivity
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})

	// The redirect route — the critical path of the entire platform.
	// Pattern: /{shortcode} maps to the resolution handler (Story 1.5).
	r.Get("/{shortcode}", func(w http.ResponseWriter, r *http.Request) {
		// TODO(story-1.5): Implement short code resolution
		shortCode := chi.URLParam(r, "shortcode")
		log.Info("redirect request received",
			slog.String("short_code", shortCode),
			slog.String("request_id", middleware.GetReqID(r.Context())),
		)
		http.Error(w, "not implemented", http.StatusNotImplemented)
	})

	srv := &http.Server{
		Addr:         ":" + cfg.RedirectPort,
		Handler:      r,
		ReadTimeout:  time.Duration(cfg.RedirectReadTimeoutS) * time.Second,
		WriteTimeout: time.Duration(cfg.RedirectWriteTimeoutS) * time.Second,
		IdleTimeout:  time.Duration(cfg.RedirectIdleTimeoutS) * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Info("http server listening", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- fmt.Errorf("http server error: %w", err)
		}
	}()

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
		time.Duration(cfg.RedirectShutdownTimeoutS)*time.Second,
	)
	defer shutdownCancel()

	log.Info("shutting down redirect server")
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown error", slog.String("error", err.Error()))
	}

	if err := otelShutdown(shutdownCtx); err != nil {
		log.Error("otel shutdown error", slog.String("error", err.Error()))
	}

	log.Info("shutdown complete")
}
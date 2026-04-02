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
	"github.com/urlshortener/platform/internal/infrastructure/postgres"
	redisinfra "github.com/urlshortener/platform/internal/infrastructure/redis"
	"github.com/urlshortener/platform/pkg/logger"
	"github.com/urlshortener/platform/pkg/telemetry"
)

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

	// ── PostgreSQL ────────────────────────────────────────────────────────────
	var dbClient *postgres.Client
	if cfg.DBPrimaryDSN != "" {
		dbCtx, dbCancel := context.WithTimeout(ctx, 15*time.Second)
		dbClient, err = postgres.New(dbCtx, cfg)
		dbCancel()
		if err != nil {
			log.Error("failed to connect to postgresql",
				slog.String("error", err.Error()),
				slog.String("hint", "run: make infra-up"),
			)
			os.Exit(1)
		}
		log.Info("postgresql connected",
			slog.Int("max_conns", int(cfg.DBMaxOpenConns)),
		)
	} else {
		log.Warn("DB_PRIMARY_DSN not set — running without database")
	}

	// ── Redis ─────────────────────────────────────────────────────────────────
	// Redis initialization follows the same fail-fast pattern as PostgreSQL.
	// A missing Redis connection means the redirect service cannot cache —
	// the readyz probe returns 503 and Kubernetes withholds traffic.
	var redisClient *redisinfra.Client
	if cfg.RedisAddr != "" {
		redisCtx, redisCancel := context.WithTimeout(ctx, 10*time.Second)
		redisClient, err = redisinfra.New(redisCtx, cfg)
		redisCancel()
		if err != nil {
			log.Error("failed to connect to redis",
				slog.String("error", err.Error()),
				slog.String("hint", "run: make infra-up"),
			)
			os.Exit(1)
		}
		log.Info("redis connected",
			slog.String("addr", cfg.RedisAddr),
			slog.Int("pool_size", cfg.RedisPoolSize),
		)
	} else {
		log.Warn("REDIS_ADDR not set — running without cache")
	}

	// ── HTTP Router ───────────────────────────────────────────────────────────
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(time.Duration(cfg.APIWriteTimeoutS) * time.Second))

	r.Get("/healthz", handleLiveness())
	r.Get("/readyz", handleReadiness(log, dbClient, redisClient))

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/ping", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok","service":"api-service"}`))
		})
		// URL CRUD handlers registered in Story 1.5
	})

	// ── HTTP Server ───────────────────────────────────────────────────────────
	srv := &http.Server{
		Addr:         ":" + cfg.APIPort,
		Handler:      r,
		ReadTimeout:  time.Duration(cfg.APIReadTimeoutS) * time.Second,
		WriteTimeout: time.Duration(cfg.APIWriteTimeoutS) * time.Second,
		IdleTimeout:  time.Duration(cfg.APIIdleTimeoutS) * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Info("http server listening", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- fmt.Errorf("http server error: %w", err)
		}
	}()

	// ── Graceful Shutdown ─────────────────────────────────────────────────────
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

	// Shutdown order:
	// 1. Stop accepting HTTP connections (no new requests start)
	// 2. Wait for in-flight requests (they may write to DB/Redis)
	// 3. Close Redis (after all writes are done)
	// 4. Close PostgreSQL (after all queries are done)
	// 5. Flush OTel spans (captures all operation spans)

	log.Info("shutting down http server")
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("http shutdown error", slog.String("error", err.Error()))
	}

	if redisClient != nil {
		log.Info("closing redis connections")
		if err := redisClient.Close(); err != nil {
			log.Error("redis close error", slog.String("error", err.Error()))
		}
	}

	if dbClient != nil {
		log.Info("closing postgresql connections")
		dbClient.Close()
	}

	log.Info("flushing telemetry spans")
	if err := otelShutdown(shutdownCtx); err != nil {
		log.Error("otel shutdown error", slog.String("error", err.Error()))
	}

	log.Info("shutdown complete")
}

// handleLiveness is the Kubernetes liveness probe.
// Process alive = 200. No dependency checks.
func handleLiveness() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"alive"}`))
	}
}

// handleReadiness is the Kubernetes readiness probe.
// Returns 503 if any critical dependency (PostgreSQL, Redis) is unhealthy.
// Kubernetes stops routing traffic to this pod when this returns non-2xx.
//
// Probe timeout budget:
//
//	Each dependency ping has a 3s timeout.
//	With 2 dependencies, worst case = 6s before returning 503.
//	Kubernetes readiness probe has a default timeout of 1s — we override
//	this to 5s in the Kubernetes manifest (Phase 4) to match.
func handleReadiness(
	log *slog.Logger,
	db *postgres.Client,
	cache *redisinfra.Client,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		pingCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		if db == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"not ready","reason":"database not configured"}`))
			return
		}
		if err := db.Ping(pingCtx); err != nil {
			log.Warn("readiness: postgresql ping failed", slog.String("error", err.Error()))
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"not ready","reason":"database unreachable"}`))
			return
		}

		if cache == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"not ready","reason":"cache not configured"}`))
			return
		}
		if err := cache.Ping(pingCtx); err != nil {
			log.Warn("readiness: redis ping failed", slog.String("error", err.Error()))
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"not ready","reason":"cache unreachable"}`))
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	}
}

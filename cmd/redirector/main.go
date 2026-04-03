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
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/urlshortener/platform/internal/application/resolve"
	"github.com/urlshortener/platform/internal/config"
	"github.com/urlshortener/platform/internal/infrastructure/postgres"
	redisinfra "github.com/urlshortener/platform/internal/infrastructure/redis"
	"github.com/urlshortener/platform/internal/interfaces/http/handler"
	httpmiddleware "github.com/urlshortener/platform/internal/interfaces/http/middleware"
	"github.com/urlshortener/platform/internal/interfaces/http/response"
	"github.com/urlshortener/platform/pkg/logger"
	"github.com/urlshortener/platform/pkg/telemetry"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

func main() {
	// ── 1. Configuration ──────────────────────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	// ── 2. Logger ─────────────────────────────────────────────────────────────
	log := logger.New(cfg.LogLevel, cfg.LogFormat).With(
		slog.String("service", "redirect-service"),
		slog.String("version", version),
		slog.String("commit", commit),
		slog.String("env", cfg.Environment),
	)
	slog.SetDefault(log)

	log.Info("starting redirect-service",
		slog.String("build_time", buildTime),
		slog.String("port", cfg.RedirectPort),
	)

	// ── 3. OpenTelemetry ──────────────────────────────────────────────────────
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

	// ── 4. PostgreSQL ─────────────────────────────────────────────────────────
	var dbClient *postgres.Client
	if cfg.DBPrimaryDSN != "" {
		dbCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		dbClient, err = postgres.New(dbCtx, cfg)
		cancel()
		if err != nil {
			log.Error("failed to connect to postgresql", slog.String("error", err.Error()))
			os.Exit(1)
		}
		log.Info("postgresql connected")
	} else {
		log.Warn("DB_PRIMARY_DSN not set — running without database")
	}

	// ── 5. Redis ──────────────────────────────────────────────────────────────
	var redisClient *redisinfra.Client
	if cfg.RedisAddr != "" {
		redisCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		redisClient, err = redisinfra.New(redisCtx, cfg)
		cancel()
		if err != nil {
			log.Error("failed to connect to redis", slog.String("error", err.Error()))
			os.Exit(1)
		}
		log.Info("redis connected", slog.String("addr", cfg.RedisAddr))
	} else {
		log.Warn("REDIS_ADDR not set — running without cache")
	}

	// ── 6. Infrastructure adapters ────────────────────────────────────────────
	// The redirect service uses ReadonlyRepository — it never writes to the DB.
	// This is enforced at the type level: postgres.URLRepository satisfies
	// both Repository and ReadonlyRepository interfaces.
	var urlRepo *postgres.URLRepository
	if dbClient != nil {
		urlRepo = postgres.NewURLRepository(dbClient)
	}

	var urlCache *redisinfra.URLCache
	if redisClient != nil {
		urlCache = redisinfra.NewURLCache(redisClient)
	}

	// ── 7. Application use case handler ───────────────────────────────────────
	// The resolve handler is always created; it handles nil cache gracefully
	// by falling back to DB-only mode (no Redis = all requests hit PostgreSQL).
	resolveHandler := resolve.NewHandler(
		urlRepo,
		urlCache,
		cfg.RedirectCacheTTLS,
		cfg.CacheNegativeTTLS,
		log,
	)

	// ── 8. HTTP handler ───────────────────────────────────────────────────────
	redirectHTTPHandler := handler.NewRedirectHandler(resolveHandler, log)

	// ── 9. Router ─────────────────────────────────────────────────────────────
	r := chi.NewRouter()

	// Redirect service middleware is intentionally leaner than API service:
	// - No Timeout middleware (redirect handler has its own short TTL via context)
	// - Recoverer still needed to prevent goroutine leaks on panic
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(httpmiddleware.OTel("redirect-service"))
	r.Use(httpmiddleware.RequestLogger(log))
	r.Use(chimiddleware.Recoverer)
	// TODO(story-1.6): add Prometheus metrics middleware here

	// Health probes
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, map[string]string{"status": "alive"})
	})

	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		pingCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		if dbClient != nil {
			if err := dbClient.Ping(pingCtx); err != nil {
				log.Warn("readiness: postgresql ping failed", slog.String("error", err.Error()))
				response.WriteProblem(w, response.Problem{
					Type:   response.ProblemTypeInternal,
					Title:  "Not Ready",
					Status: http.StatusServiceUnavailable,
					Detail: "database unreachable",
				})
				return
			}
		}
		if redisClient != nil {
			if err := redisClient.Ping(pingCtx); err != nil {
				log.Warn("readiness: redis ping failed", slog.String("error", err.Error()))
				response.WriteProblem(w, response.Problem{
					Type:   response.ProblemTypeInternal,
					Title:  "Not Ready",
					Status: http.StatusServiceUnavailable,
					Detail: "cache unreachable",
				})
				return
			}
		}
		response.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})

	// ── The redirect route — the most-hit route in the entire platform ─────────
	// Pattern: /{shortcode}
	// All single-segment paths are treated as short codes.
	// Multi-segment paths (/api/v1/..., /healthz) are registered first and
	// take priority because chi routes are matched in registration order
	// within the same specificity level.
	r.Get("/{shortcode}", redirectHTTPHandler.Handle)

	// ── 10. HTTP Server ───────────────────────────────────────────────────────
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

	// ── 11. Graceful Shutdown ─────────────────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		log.Error("server error", slog.String("error", err.Error()))
	case sig := <-quit:
		log.Info("shutdown signal received", slog.String("signal", sig.String()))
	}

	shutdownCtx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(cfg.RedirectShutdownTimeoutS)*time.Second,
	)
	defer cancel()

	log.Info("shutting down redirect server")
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown error", slog.String("error", err.Error()))
	}

	if redisClient != nil {
		if err := redisClient.Close(); err != nil {
			log.Error("redis close error", slog.String("error", err.Error()))
		}
	}
	if dbClient != nil {
		dbClient.Close()
	}

	if err := otelShutdown(shutdownCtx); err != nil {
		log.Error("otel shutdown error", slog.String("error", err.Error()))
	}

	log.Info("shutdown complete")
}

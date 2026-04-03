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

	"github.com/urlshortener/platform/internal/application/shorten"
	"github.com/urlshortener/platform/internal/config"
	"github.com/urlshortener/platform/internal/infrastructure/postgres"
	redisinfra "github.com/urlshortener/platform/internal/infrastructure/redis"
	"github.com/urlshortener/platform/internal/interfaces/http/handler"
	httpmiddleware "github.com/urlshortener/platform/internal/interfaces/http/middleware"
	"github.com/urlshortener/platform/internal/interfaces/http/response"
	"github.com/urlshortener/platform/pkg/logger"
	"github.com/urlshortener/platform/pkg/shortcode"
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
		slog.String("service", cfg.ServiceName),
		slog.String("version", version),
		slog.String("commit", commit),
		slog.String("env", cfg.Environment),
	)
	slog.SetDefault(log) // Set as default so library code uses our logger

	log.Info("starting api-service",
		slog.String("build_time", buildTime),
		slog.String("port", cfg.APIPort),
	)

	// ── 3. OpenTelemetry ──────────────────────────────────────────────────────
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
		log.Info("postgresql connected", slog.Int("max_conns", int(cfg.DBMaxOpenConns)))
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
	// Only wire adapters when infrastructure is available.
	// Nil adapters are handled gracefully by the use case handlers.
	var urlRepo *postgres.URLRepository
	if dbClient != nil {
		urlRepo = postgres.NewURLRepository(dbClient)
	}

	var urlCache *redisinfra.URLCache
	if redisClient != nil {
		urlCache = redisinfra.NewURLCache(redisClient)
	}

	// ── 7. Application use case handlers ─────────────────────────────────────
	// Short code generator — shared by the shorten handler.
	codeGenerator := shortcode.New(cfg.ShortCodeLength)

	// ShortenURL use case handler.
	// Wire: HTTP handler → use case handler → domain ports → infrastructure adapters.
	var shortenHandler *shorten.Handler
	if urlRepo != nil {
		shortenHandler = shorten.NewHandler(
			urlRepo,
			urlCache, // may be nil (shorten still works, no cache pre-warm)
			codeGenerator,
			cfg.BaseURL,
			cfg.RedirectCacheTTLS,
			log,
		)
	}

	// ── 8. HTTP router and handlers ───────────────────────────────────────────
	r := chi.NewRouter()

	// ── Global middleware chain ────────────────────────────────────────────────
	// Order is deliberate:
	//   RequestID:     must be first so all subsequent middleware can read the ID
	//   RealIP:        must be early so IP is resolved before logging
	//   OTel:          must be before logger so trace_id is available for log enrichment
	//   RequestLogger: uses request_id (set above) and enriches context with logger
	//   Recoverer:     must be after logger so panics are logged with context
	//   Timeout:       enforces write deadline — must wrap the actual handler

	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(httpmiddleware.OTel(cfg.ServiceName))
	r.Use(httpmiddleware.RequestLogger(log))
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.Timeout(time.Duration(cfg.APIWriteTimeoutS) * time.Second))

	// ── Health probes ─────────────────────────────────────────────────────────
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, map[string]string{"status": "alive"})
	})

	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		pingCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		if dbClient == nil {
			response.WriteProblem(w, response.Problem{
				Type:   response.ProblemTypeInternal,
				Title:  "Not Ready",
				Status: http.StatusServiceUnavailable,
				Detail: "database not configured",
			})
			return
		}
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
		if redisClient == nil {
			response.WriteProblem(w, response.Problem{
				Type:   response.ProblemTypeInternal,
				Title:  "Not Ready",
				Status: http.StatusServiceUnavailable,
				Detail: "cache not configured",
			})
			return
		}
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
		response.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})

	// ── API routes ────────────────────────────────────────────────────────────
	r.Route("/api/v1", func(r chi.Router) {
		// URL resource
		if shortenHandler != nil {
			r.Post("/urls", handler.NewShortenHandler(shortenHandler, log).Handle)
		} else {
			// Respond with 503 if dependencies are missing — better than 404.
			r.Post("/urls", func(w http.ResponseWriter, r *http.Request) {
				response.WriteProblem(w, response.Problem{
					Type:   response.ProblemTypeInternal,
					Title:  "Service Unavailable",
					Status: http.StatusServiceUnavailable,
					Detail: "Database not available. Run: make infra-up",
				})
			})
		}

		// TODO(story-2.x): GET /urls (list), GET /urls/{id}, PATCH /urls/{id}, DELETE /urls/{id}
		// TODO(story-2.x): POST /workspaces, GET /workspaces/{id}/members
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
		log.Error("server error", slog.String("error", err.Error()))
	case sig := <-quit:
		log.Info("shutdown signal received", slog.String("signal", sig.String()))
	}

	shutdownCtx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(cfg.APIShutdownTimeoutS)*time.Second,
	)
	defer cancel()

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

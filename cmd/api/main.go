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
	"github.com/urlshortener/platform/internal/infrastructure/metrics"
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
	slog.SetDefault(log)

	log.Info("starting api-service",
		slog.String("build_time", buildTime),
		slog.String("port", cfg.APIPort),
		slog.String("metrics_port", cfg.MetricsPort),
	)

	ctx := context.Background()

	// ── OpenTelemetry ─────────────────────────────────────────────────────────
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

	// ── Prometheus metrics ────────────────────────────────────────────────────
	// Initialized before infrastructure so build_info is always present
	// even if DB/Redis connections fail.
	appMetrics := metrics.New(cfg.ServiceName, version, commit)

	// ── PostgreSQL ────────────────────────────────────────────────────────────
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

	// ── Redis ─────────────────────────────────────────────────────────────────
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

	// ── Infrastructure adapters ───────────────────────────────────────────────
	var urlRepo *postgres.URLRepository
	if dbClient != nil {
		urlRepo = postgres.NewURLRepository(dbClient)
	}

	var urlCache *redisinfra.URLCache
	if redisClient != nil {
		urlCache = redisinfra.NewURLCache(redisClient)
	}

	// ── Application layer ─────────────────────────────────────────────────────
	codeGenerator := shortcode.New(cfg.ShortCodeLength)
	var shortenUseCase *shorten.Handler
	if urlRepo != nil {
		shortenUseCase = shorten.NewHandler(
			urlRepo, urlCache, codeGenerator,
			cfg.BaseURL, cfg.RedirectCacheTTLS, log,
		)
	}

	// ── HTTP router ───────────────────────────────────────────────────────────
	r := chi.NewRouter()

	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(httpmiddleware.OTel(cfg.ServiceName))
	r.Use(httpmiddleware.RequestLogger(log))
	// Metrics middleware MUST be before Recoverer so panic-induced 500s
	// are counted. See metrics.go for the detailed explanation.
	r.Use(httpmiddleware.Metrics(appMetrics, cfg.ServiceName))
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.Timeout(time.Duration(cfg.APIWriteTimeoutS) * time.Second))

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, map[string]string{"status": "alive"})
	})

	r.Get("/readyz", readyHandler(log, dbClient, redisClient))

	r.Route("/api/v1", func(r chi.Router) {
		if shortenUseCase != nil {
			r.Post("/urls",
				handler.NewShortenHandler(shortenUseCase, log, appMetrics).Handle)
		} else {
			r.Post("/urls", func(w http.ResponseWriter, r *http.Request) {
				response.WriteProblem(w, response.Problem{
					Type: response.ProblemTypeInternal, Title: "Service Unavailable",
					Status: http.StatusServiceUnavailable, Detail: "Database not available.",
				})
			})
		}
	})

	// ── Separate metrics HTTP server on MetricsPort ───────────────────────────
	// Running on a dedicated port means:
	//   - /metrics is never exposed through WSO2 or the public Ingress
	//   - Prometheus scrapes it directly from the pod's pod IP
	//   - The application HTTP server is not burdened with metrics scrape traffic
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", appMetrics.Handler())
	metricsSrv := &http.Server{
		Addr:    ":" + cfg.MetricsPort,
		Handler: metricsMux,
	}

	// ── Application HTTP server ───────────────────────────────────────────────
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

	go func() {
		log.Info("metrics server listening", slog.String("addr", metricsSrv.Addr))
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics server error", slog.String("error", err.Error()))
		}
	}()

	// ── Background: pool stats collector ─────────────────────────────────────
	// Updates DB and Redis pool gauges every 15s so Prometheus scrapes
	// current values without us having to instrument every query.
	// Uses a context that gets cancelled on shutdown to stop the goroutine cleanly.
	statsCtx, statsCancel := context.WithCancel(ctx)
	go collectPoolStats(statsCtx, appMetrics, dbClient, redisClient)

	// ── Graceful shutdown ─────────────────────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		log.Error("server error", slog.String("error", err.Error()))
	case sig := <-quit:
		log.Info("shutdown signal received", slog.String("signal", sig.String()))
	}

	// Stop the pool stats goroutine before closing connections.
	statsCancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(
		context.Background(),
		time.Duration(cfg.APIShutdownTimeoutS)*time.Second,
	)
	defer shutdownCancel()

	log.Info("shutting down http server")
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("http shutdown error", slog.String("error", err.Error()))
	}

	log.Info("shutting down metrics server")
	if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("metrics shutdown error", slog.String("error", err.Error()))
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

// readyHandler returns the Kubernetes readiness probe handler.
func readyHandler(
	log *slog.Logger,
	db *postgres.Client,
	cache *redisinfra.Client,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pingCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		if db == nil {
			response.WriteProblem(w, response.Problem{
				Type: response.ProblemTypeInternal, Title: "Not Ready",
				Status: http.StatusServiceUnavailable, Detail: "database not configured",
			})
			return
		}
		if err := db.Ping(pingCtx); err != nil {
			log.Warn("readiness: postgresql ping failed", slog.String("error", err.Error()))
			response.WriteProblem(w, response.Problem{
				Type: response.ProblemTypeInternal, Title: "Not Ready",
				Status: http.StatusServiceUnavailable, Detail: "database unreachable",
			})
			return
		}
		if cache == nil {
			response.WriteProblem(w, response.Problem{
				Type: response.ProblemTypeInternal, Title: "Not Ready",
				Status: http.StatusServiceUnavailable, Detail: "cache not configured",
			})
			return
		}
		if err := cache.Ping(pingCtx); err != nil {
			log.Warn("readiness: redis ping failed", slog.String("error", err.Error()))
			response.WriteProblem(w, response.Problem{
				Type: response.ProblemTypeInternal, Title: "Not Ready",
				Status: http.StatusServiceUnavailable, Detail: "cache unreachable",
			})
			return
		}
		response.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
	}
}

// collectPoolStats runs until ctx is cancelled, updating pool stat gauges
// on a 15-second tick. This is the correct frequency — more frequent updates
// would add overhead; less frequent would make the Prometheus scrape see stale values.
func collectPoolStats(
	ctx context.Context,
	m *metrics.Metrics,
	db *postgres.Client,
	cache *redisinfra.Client,
) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if db != nil {
				s := db.PrimaryStats()
				m.UpdateDBPoolStats("primary", s.TotalConns, s.IdleConns, s.AcquiredConns, s.MaxConns)
			}
			if cache != nil {
				s := cache.Stats()
				m.UpdateCachePoolStats(s.TotalConns, s.IdleConns, s.StaleConns)
			}
		}
	}
}

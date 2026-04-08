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

	appanalytics "github.com/urlshortener/platform/internal/application/analytics"
	"github.com/urlshortener/platform/internal/application/resolve"
	"github.com/urlshortener/platform/internal/config"
	"github.com/urlshortener/platform/internal/infrastructure/metrics"
	"github.com/urlshortener/platform/internal/infrastructure/postgres"
	redisinfra "github.com/urlshortener/platform/internal/infrastructure/redis"
	"github.com/urlshortener/platform/internal/interfaces/http/handler"
	httpmiddleware "github.com/urlshortener/platform/internal/interfaces/http/middleware"
	"github.com/urlshortener/platform/internal/interfaces/http/response"
	"github.com/urlshortener/platform/pkg/iphasher"
	"github.com/urlshortener/platform/pkg/logger"
	"github.com/urlshortener/platform/pkg/telemetry"

	"github.com/urlshortener/platform/internal/domain/ratelimit"
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

	redirectMetricsPort := "9091"

	log := logger.New(cfg.LogLevel, cfg.LogFormat).With(
		slog.String("service", "redirect-service"),
		slog.String("version", version),
		slog.String("commit", commit),
		slog.String("env", cfg.Environment),
	)
	slog.SetDefault(log)
	log.Info("starting redirect-service",
		slog.String("port", cfg.RedirectPort),
		slog.String("build_time", buildTime),
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
		log.Error("otel init failed", slog.String("error", err.Error()))
		os.Exit(1)
	}

	appMetrics := metrics.New("redirect-service", version, commit)

	// ── PostgreSQL ────────────────────────────────────────────────────────────
	var dbClient *postgres.Client
	if cfg.DBPrimaryDSN != "" {
		dbCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		dbClient, err = postgres.New(dbCtx, cfg)
		cancel()
		if err != nil {
			log.Error("db connect failed", slog.String("error", err.Error()))
			os.Exit(1)
		}
		log.Info("postgresql connected")
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
			log.Error("redis connect failed", slog.String("error", err.Error()))
			os.Exit(1)
		}
		log.Info("redis connected")
	} else {
		log.Warn("REDIS_ADDR not set — running without cache")
	}

	// ── Rate limiter ──────────────────────────────────────────────────────────
	var tokenBucketLimiter *redisinfra.TokenBucketLimiter
	if redisClient != nil {
		tokenBucketLimiter = redisinfra.NewTokenBucketLimiter(redisClient)
		log.Info("rate limiter enabled (redirect class)")
	}

	// ── Infrastructure adapters ───────────────────────────────────────────────
	var urlRepo *postgres.URLRepository
	var analyticsRepo *postgres.AnalyticsRepository

	if dbClient != nil {
		urlRepo = postgres.NewURLRepository(dbClient)
		analyticsRepo = postgres.NewAnalyticsRepository(dbClient)
	}

	var urlCache *redisinfra.URLCache
	if redisClient != nil {
		urlCache = redisinfra.NewURLCache(redisClient)
	}

	// ── Analytics ingestion service ───────────────────────────────────────────
	// The analytics service is the key addition in Story 3.1.
	// It starts its background drainer goroutine immediately and runs until
	// analyticsCancel() is called during shutdown.
	//
	// Shutdown ordering:
	//   1. Stop HTTP server (no more redirects → no more events produced)
	//   2. analyticsCancel() → drainer goroutine exits
	//   3. analyticsService.Shutdown() → drain remaining channel events
	//   4. Close DB connection (analytics writes complete)
	//
	// IP hash salt is loaded from environment (IP_HASH_SALT).
	// If unset, hashing still works but privacy guarantees are weaker.
	analyticsCtx, analyticsCancel := context.WithCancel(ctx)
	var analyticsSvc *appanalytics.Service

	if analyticsRepo != nil {
		ipHashSalt := getEnvOrDefault("IP_HASH_SALT", "")
		if ipHashSalt == "" {
			log.Warn("IP_HASH_SALT not set — IP hashing uses empty secret (weaker privacy)")
		}
		hasher := iphasher.New(ipHashSalt)
		analyticsSvc = appanalytics.NewService(analyticsCtx, analyticsRepo, hasher, log)
		log.Info("analytics ingestion service started")
	} else {
		log.Warn("analytics service disabled — database not configured")
		analyticsCancel() // cancel unused context immediately
	}

	// ── Application layer ─────────────────────────────────────────────────────
	resolveUseCase := resolve.NewHandler(
		urlRepo, urlCache,
		cfg.RedirectCacheTTLS, cfg.CacheNegativeTTLS, log,
	)

	// Build the redirect HTTP handler with analytics wired in.
	// analyticsSvc may be nil — handler.NewRedirectHandler is nil-safe.
	redirectHTTPHandler := handler.NewRedirectHandler(
		resolveUseCase,
		log,
		analyticsSvc, // nil when DB not configured
		appMetrics,
	)

	// ── Rate limit middleware ─────────────────────────────────────────────────
	var effectiveLimiter httpmiddleware.Limiter
	if tokenBucketLimiter != nil {
		effectiveLimiter = tokenBucketLimiter
	} else {
		effectiveLimiter = &noopLimiter{}
	}

	rlRedirect := httpmiddleware.RateLimit(httpmiddleware.RateLimitConfig{
		Limiter:       effectiveLimiter,
		ServiceName:   "redirect-service",
		EndpointClass: ratelimit.ClassRedirect,
		Log:           log,
		FailOpen:      true, // Redis blip must NEVER cause redirect outage
	})

	// ── Router ────────────────────────────────────────────────────────────────
	r := chi.NewRouter()

	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(httpmiddleware.OTel("redirect-service"))
	r.Use(httpmiddleware.RequestLogger(log))
	r.Use(httpmiddleware.Metrics(appMetrics, "redirect-service"))
	r.Use(chimiddleware.Recoverer)

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, map[string]string{"status": "alive"})
	})

	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		pingCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		if dbClient != nil {
			if err := dbClient.Ping(pingCtx); err != nil {
				log.Warn("readiness: db ping failed", slog.String("error", err.Error()))
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

	// The redirect route — rate limited, analytics captured.
	// Health probes above are deliberately outside the rate limit.
	r.With(rlRedirect).Get("/{shortcode}", redirectHTTPHandler.Handle)

	// ── Servers ───────────────────────────────────────────────────────────────
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", appMetrics.Handler())
	metricsSrv := &http.Server{Addr: ":" + redirectMetricsPort, Handler: metricsMux}

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
			serverErr <- fmt.Errorf("http: %w", err)
		}
	}()
	go func() {
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics server error", slog.String("error", err.Error()))
		}
	}()

	// Pool stats background collector
	statsCtx, statsCancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-statsCtx.Done():
				return
			case <-ticker.C:
				if dbClient != nil {
					s := dbClient.PrimaryStats()
					appMetrics.UpdateDBPoolStats("primary", s.TotalConns, s.IdleConns, s.AcquiredConns, s.MaxConns)
				}
				if redisClient != nil {
					s := redisClient.Stats()
					appMetrics.UpdateCachePoolStats(s.TotalConns, s.IdleConns, s.StaleConns)
				}
			}
		}
	}()

	// ── Graceful shutdown ─────────────────────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		log.Error("server error", slog.String("error", err.Error()))
	case sig := <-quit:
		log.Info("shutdown signal received", slog.String("signal", sig.String()))
	}

	statsCancel()

	shutdownCtx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(cfg.RedirectShutdownTimeoutS)*time.Second,
	)
	defer cancel()

	// Shutdown order (analytics must flush before DB closes):
	log.Info("stopping http server")
	_ = srv.Shutdown(shutdownCtx)
	_ = metricsSrv.Shutdown(shutdownCtx)

	if analyticsSvc != nil {
		log.Info("flushing analytics events")
		analyticsCancel()
		analyticsSvc.Shutdown(shutdownCtx)
	}

	if redisClient != nil {
		_ = redisClient.Close()
	}
	if dbClient != nil {
		dbClient.Close()
	}
	_ = otelShutdown(shutdownCtx)
	log.Info("shutdown complete")
}

// noopLimiter allows all requests — used when Redis is unavailable.
type noopLimiter struct{}

func (n *noopLimiter) Check(_ context.Context, _ string, policy ratelimit.Policy) (*ratelimit.Result, error) {
	return &ratelimit.Result{
		Allowed:   true,
		Remaining: policy.BucketCapacity(),
		Limit:     policy.BucketCapacity(),
		ResetAt:   time.Now().Add(policy.Window),
	}, nil
}

func getEnvOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

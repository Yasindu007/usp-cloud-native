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
	"github.com/urlshortener/platform/internal/infrastructure/metrics"
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
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	// The redirect service uses a different metrics port (9091) from the
	// API service (9090) so both can run on the same host simultaneously.
	redirectMetricsPort := "9091"
	if p := cfg.MetricsPort; p != "9090" {
		redirectMetricsPort = p
	}

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
		slog.String("metrics_port", redirectMetricsPort),
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

	appMetrics := metrics.New("redirect-service", version, commit)

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
	}

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
	}

	var urlRepo *postgres.URLRepository
	if dbClient != nil {
		urlRepo = postgres.NewURLRepository(dbClient)
	}

	var urlCache *redisinfra.URLCache
	if redisClient != nil {
		urlCache = redisinfra.NewURLCache(redisClient)
	}

	resolveUseCase := resolve.NewHandler(
		urlRepo, urlCache,
		cfg.RedirectCacheTTLS, cfg.CacheNegativeTTLS, log,
	)

	redirectHTTPHandler := handler.NewRedirectHandler(resolveUseCase, log, appMetrics)

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
				log.Warn("readiness: postgresql ping failed", slog.String("error", err.Error()))
				response.WriteProblem(w, response.Problem{
					Type: response.ProblemTypeInternal, Title: "Not Ready",
					Status: http.StatusServiceUnavailable, Detail: "database unreachable",
				})
				return
			}
		}
		if redisClient != nil {
			if err := redisClient.Ping(pingCtx); err != nil {
				log.Warn("readiness: redis ping failed", slog.String("error", err.Error()))
				response.WriteProblem(w, response.Problem{
					Type: response.ProblemTypeInternal, Title: "Not Ready",
					Status: http.StatusServiceUnavailable, Detail: "cache unreachable",
				})
				return
			}
		}
		response.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})

	r.Get("/{shortcode}", redirectHTTPHandler.Handle)

	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", appMetrics.Handler())
	metricsSrv := &http.Server{
		Addr:    ":" + redirectMetricsPort,
		Handler: metricsMux,
	}

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

	go func() {
		log.Info("metrics server listening", slog.String("addr", metricsSrv.Addr))
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics server error", slog.String("error", err.Error()))
		}
	}()

	statsCtx, statsCancel := context.WithCancel(ctx)
	go collectPoolStats(statsCtx, appMetrics, dbClient, redisClient)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		log.Error("server error", slog.String("error", err.Error()))
	case sig := <-quit:
		log.Info("shutdown signal received", slog.String("signal", sig.String()))
	}

	statsCancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(
		context.Background(),
		time.Duration(cfg.RedirectShutdownTimeoutS)*time.Second,
	)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown error", slog.String("error", err.Error()))
	}
	if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("metrics shutdown error", slog.String("error", err.Error()))
	}
	if redisClient != nil {
		_ = redisClient.Close()
	}
	if dbClient != nil {
		dbClient.Close()
	}
	if err := otelShutdown(shutdownCtx); err != nil {
		log.Error("otel shutdown error", slog.String("error", err.Error()))
	}

	log.Info("shutdown complete")
}

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

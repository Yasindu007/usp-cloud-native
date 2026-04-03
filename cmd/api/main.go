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
	domainauth "github.com/urlshortener/platform/internal/domain/auth"
	infraauth "github.com/urlshortener/platform/internal/infrastructure/auth"
	"github.com/urlshortener/platform/internal/infrastructure/metrics"
	"github.com/urlshortener/platform/internal/infrastructure/postgres"
	redisinfra "github.com/urlshortener/platform/internal/infrastructure/redis"
	"github.com/urlshortener/platform/internal/interfaces/http/handler"
	httpmiddleware "github.com/urlshortener/platform/internal/interfaces/http/middleware"
	"github.com/urlshortener/platform/internal/interfaces/http/response"
	"github.com/urlshortener/platform/pkg/jwtutil"
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
	slog.SetDefault(log)

	log.Info("starting api-service",
		slog.String("build_time", buildTime),
		slog.String("port", cfg.APIPort),
		slog.String("metrics_port", cfg.MetricsPort),
	)

	ctx := context.Background()

	// ── 3. OpenTelemetry ──────────────────────────────────────────────────────
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

	// ── 4. Prometheus metrics ─────────────────────────────────────────────────
	appMetrics := metrics.New(cfg.ServiceName, version, commit)

	// ── 5. PostgreSQL ─────────────────────────────────────────────────────────
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

	// ── 6. Redis ──────────────────────────────────────────────────────────────
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

	// ── 7. JWT auth configuration ─────────────────────────────────────────────
	// Load the public key for JWT verification.
	// The deny list is wired to Redis if available.
	//
	// Auth is optional in development:
	//   - If JWT_PUBLIC_KEY_PATH is empty → no auth middleware applied
	//   - Handlers fall back to X-Workspace-ID / X-User-ID headers
	//   - WARNING: never run in production without a public key path
	var authCfg *httpmiddleware.AuthConfig

	if cfg.JWTPublicKeyPath != "" {
		keySet, err := jwtutil.LoadPublicKeyAsJWKSet(cfg.JWTPublicKeyPath)
		if err != nil {
			log.Error("failed to load JWT public key",
				slog.String("path", cfg.JWTPublicKeyPath),
				slog.String("error", err.Error()),
			)
			os.Exit(1)
		}

		ac := httpmiddleware.AuthConfig{
			Issuer:   cfg.JWTIssuer,
			Audience: cfg.JWTAudience,
			KeySet:   keySet,
			Log:      log,
		}

		// Wire deny list if Redis is available.
		if redisClient != nil {
			ac.DenyList = infraauth.NewDenyList(redisClient.RDB())
			log.Info("jwt deny list enabled (redis-backed)")
		} else {
			log.Warn("jwt deny list disabled (redis not configured) — tokens cannot be revoked")
		}

		authCfg = &ac
		log.Info("jwt authentication enabled",
			slog.String("issuer", cfg.JWTIssuer),
			slog.String("audience", cfg.JWTAudience),
			slog.String("public_key", cfg.JWTPublicKeyPath),
		)
	} else {
		log.Warn("JWT_PUBLIC_KEY_PATH not set — authentication DISABLED (development mode only)")
		if cfg.IsProduction() {
			log.Error("JWT_PUBLIC_KEY_PATH is required in production")
			os.Exit(1)
		}
	}

	// ── 8. Infrastructure adapters ────────────────────────────────────────────
	var urlRepo *postgres.URLRepository
	if dbClient != nil {
		urlRepo = postgres.NewURLRepository(dbClient)
	}

	var urlCache *redisinfra.URLCache
	if redisClient != nil {
		urlCache = redisinfra.NewURLCache(redisClient)
	}

	// ── 9. Application use case handlers ─────────────────────────────────────
	codeGenerator := shortcode.New(cfg.ShortCodeLength)
	var shortenUseCase *shorten.Handler
	if urlRepo != nil {
		shortenUseCase = shorten.NewHandler(
			urlRepo, urlCache, codeGenerator,
			cfg.BaseURL, cfg.RedirectCacheTTLS, log,
		)
	}

	// ── 10. HTTP router ───────────────────────────────────────────────────────
	r := chi.NewRouter()

	// Global middleware — applied to ALL routes including health probes.
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(httpmiddleware.OTel(cfg.ServiceName))
	r.Use(httpmiddleware.RequestLogger(log))
	r.Use(httpmiddleware.Metrics(appMetrics, cfg.ServiceName))
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.Timeout(time.Duration(cfg.APIWriteTimeoutS) * time.Second))

	// Health probes — NO auth required.
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, map[string]string{"status": "alive"})
	})
	r.Get("/readyz", readyHandler(log, dbClient, redisClient))

	// API routes — auth middleware applied to this sub-router only.
	r.Route("/api/v1", func(r chi.Router) {
		// Apply JWT auth if configured.
		// When authCfg is nil (dev mode without keys), no auth middleware runs
		// and handlers use header-based identity fallback.
		if authCfg != nil {
			r.Use(httpmiddleware.Authenticate(*authCfg))
		}

		// POST /api/v1/urls — requires "write" scope
		if shortenUseCase != nil {
			r.With(httpmiddleware.RequireScope("write")).
				Post("/urls",
					handler.NewShortenHandler(shortenUseCase, log, appMetrics).Handle)
		} else {
			r.Post("/urls", func(w http.ResponseWriter, r *http.Request) {
				response.WriteProblem(w, response.Problem{
					Type:   response.ProblemTypeInternal,
					Title:  "Service Unavailable",
					Status: http.StatusServiceUnavailable,
					Detail: "Database not available. Run: make infra-up",
				})
			})
		}

		// Token revocation endpoint.
		// DELETE /api/v1/auth/token — add current token to deny list (logout).
		// Requires a valid token (auth middleware) to revoke itself.
		if authCfg != nil && redisClient != nil {
			denyList := infraauth.NewDenyList(redisClient.RDB())
			r.Delete("/auth/token", revokeTokenHandler(denyList, log))
		}

		// TODO(story-2.2): GET/PATCH/DELETE /urls/{id} — workspace-scoped CRUD
		// TODO(story-2.2): POST /workspaces, GET /workspaces/{id}/members
	})

	// ── 11. Metrics server ────────────────────────────────────────────────────
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", appMetrics.Handler())
	metricsSrv := &http.Server{Addr: ":" + cfg.MetricsPort, Handler: metricsMux}

	// ── 12. Application server ────────────────────────────────────────────────
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

	// Pool stats background collector.
	statsCtx, statsCancel := context.WithCancel(ctx)
	go collectPoolStats(statsCtx, appMetrics, dbClient, redisClient)

	// ── 13. Graceful shutdown ─────────────────────────────────────────────────
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
		time.Duration(cfg.APIShutdownTimeoutS)*time.Second,
	)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("http shutdown error", slog.String("error", err.Error()))
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

// revokeTokenHandler handles DELETE /api/v1/auth/token.
// Adds the caller's JTI to the deny list (logout / token revocation).
// The auth middleware has already validated the token, so claims are
// always present by the time this handler runs.
func revokeTokenHandler(dl *infraauth.DenyList, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := domainauth.FromContext(r.Context())
		if !ok {
			response.WriteProblem(w, response.Problem{
				Type:   response.ProblemTypeUnauthenticated,
				Title:  "Unauthorized",
				Status: http.StatusUnauthorized,
				Detail: "No authentication claims found.",
			})
			return
		}

		if err := dl.Revoke(r.Context(), claims.TokenID, claims.ExpiresAt); err != nil {
			log.Error("failed to revoke token",
				slog.String("jti", claims.TokenID),
				slog.String("error", err.Error()),
			)
			response.InternalError(w, r.URL.Path)
			return
		}

		log.Info("token revoked",
			slog.String("user_id", claims.UserID),
			slog.String("jti", claims.TokenID),
		)

		w.WriteHeader(http.StatusNoContent)
	}
}

func readyHandler(log *slog.Logger, db *postgres.Client, cache *redisinfra.Client) http.HandlerFunc {
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

func collectPoolStats(ctx context.Context, m *metrics.Metrics, db *postgres.Client, cache *redisinfra.Client) {
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

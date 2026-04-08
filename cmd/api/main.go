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

	appkey "github.com/urlshortener/platform/internal/application/apikey"
	"github.com/urlshortener/platform/internal/application/shorten"
	appworkspace "github.com/urlshortener/platform/internal/application/workspace"
	"github.com/urlshortener/platform/internal/config"
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

	domainauth "github.com/urlshortener/platform/internal/domain/auth"
	"github.com/urlshortener/platform/internal/domain/ratelimit"
	domainworkspace "github.com/urlshortener/platform/internal/domain/workspace"
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
	log.Info("starting api-service", slog.String("port", cfg.APIPort))

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
		log.Error("otel init failed", slog.String("error", err.Error()))
		os.Exit(1)
	}

	appMetrics := metrics.New(cfg.ServiceName, version, commit)

	// ── Infrastructure ────────────────────────────────────────────────────────
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
	}

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
	}

	// ── Rate limiter ──────────────────────────────────────────────────────────
	// The rate limiter is a thin wrapper around the Redis client.
	// It is nil-safe — middleware handles nil limiter gracefully (fail-open).
	var tokenBucketLimiter *redisinfra.TokenBucketLimiter
	if redisClient != nil {
		tokenBucketLimiter = redisinfra.NewTokenBucketLimiter(redisClient)
		log.Info("rate limiter enabled (redis token bucket)")
	} else {
		log.Warn("rate limiter disabled — Redis not configured")
	}

	// ── JWT auth config ────────────────────────────────────────────────────────
	var authCfg *httpmiddleware.AuthConfig
	if cfg.JWTPublicKeyPath != "" {
		keySet, err := jwtutil.LoadPublicKeyAsJWKSet(cfg.JWTPublicKeyPath)
		if err != nil {
			log.Error("jwt key load failed", slog.String("error", err.Error()))
			os.Exit(1)
		}
		ac := httpmiddleware.AuthConfig{
			Issuer: cfg.JWTIssuer, Audience: cfg.JWTAudience,
			KeySet: keySet, Log: log,
		}
		if redisClient != nil {
			ac.DenyList = infraauth.NewDenyList(redisClient.RDB())
		}
		authCfg = &ac
		log.Info("jwt authentication enabled")
	} else {
		log.Warn("JWT_PUBLIC_KEY_PATH not set — auth DISABLED")
		if cfg.IsProduction() {
			os.Exit(1)
		}
	}

	// ── Adapters ──────────────────────────────────────────────────────────────
	var urlRepo *postgres.URLRepository
	var wsRepo *postgres.WorkspaceRepository
	var keyRepo *postgres.APIKeyRepository
	if dbClient != nil {
		urlRepo = postgres.NewURLRepository(dbClient)
		wsRepo = postgres.NewWorkspaceRepository(dbClient)
		keyRepo = postgres.NewAPIKeyRepository(dbClient)
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

	var (
		wsCreateH   *appworkspace.CreateHandler
		wsGetH      *appworkspace.GetHandler
		wsListH     *appworkspace.ListHandler
		memberAddH  *appworkspace.AddMemberHandler
		memberListH *appworkspace.ListMembersHandler
		keyCreateH  *appkey.CreateHandler
		keyRevokeH  *appkey.RevokeHandler
		keyListH    *appkey.ListHandler
	)
	if wsRepo != nil {
		wsCreateH = appworkspace.NewCreateHandler(wsRepo, log)
		wsGetH = appworkspace.NewGetHandler(wsRepo, wsRepo)
		wsListH = appworkspace.NewListHandler(wsRepo, wsRepo)
		memberAddH = appworkspace.NewAddMemberHandler(wsRepo, wsRepo, log)
		memberListH = appworkspace.NewListMembersHandler(wsRepo)
	}
	if keyRepo != nil && wsRepo != nil {
		keyCreateH = appkey.NewCreateHandler(keyRepo, log)
		keyRevokeH = appkey.NewRevokeHandler(keyRepo, wsRepo, log)
		keyListH = appkey.NewListHandler(keyRepo, wsRepo)
	}

	// ── Rate limit middleware factories ────────────────────────────────────────
	// We create one middleware instance per endpoint class.
	// Each class has different token bucket parameters (policy matrix).
	//
	// The limiter is nil-safe: if tokenBucketLimiter is nil, we pass a
	// no-op limiter that always returns allowed=true (fail-open by design).
	var effectiveLimiter httpmiddleware.Limiter
	if tokenBucketLimiter != nil {
		effectiveLimiter = tokenBucketLimiter
	} else {
		effectiveLimiter = &noopLimiter{}
	}

	rlRead := httpmiddleware.RateLimit(httpmiddleware.RateLimitConfig{
		Limiter:       effectiveLimiter,
		ServiceName:   cfg.ServiceName,
		Metrics:       appMetrics,
		EndpointClass: ratelimit.ClassRead,
		Log:           log,
		FailOpen:      true,
	})
	rlWrite := httpmiddleware.RateLimit(httpmiddleware.RateLimitConfig{
		Limiter:       effectiveLimiter,
		ServiceName:   cfg.ServiceName,
		Metrics:       appMetrics,
		EndpointClass: ratelimit.ClassWrite,
		Log:           log,
		FailOpen:      true,
	})

	// ── Router ────────────────────────────────────────────────────────────────
	r := chi.NewRouter()

	// Global middleware — applied to all routes.
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(httpmiddleware.OTel(cfg.ServiceName))
	r.Use(httpmiddleware.RequestLogger(log))
	r.Use(httpmiddleware.Metrics(appMetrics, cfg.ServiceName))
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.Timeout(time.Duration(cfg.APIWriteTimeoutS) * time.Second))

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, map[string]string{"status": "alive"})
	})
	r.Get("/readyz", readyHandler(log, dbClient, redisClient))

	r.Route("/api/v1", func(r chi.Router) {
		// ── Authentication (dual: API key → JWT) ──────────────────────────────
		if keyRepo != nil {
			r.Use(httpmiddleware.APIKeyAuth(keyRepo, log))
		}
		if authCfg != nil {
			r.Use(httpmiddleware.Authenticate(*authCfg))
		}

		// Token revocation
		if authCfg != nil && redisClient != nil {
			dl := infraauth.NewDenyList(redisClient.RDB())
			r.Delete("/auth/token", revokeTokenHandler(dl, log))
		}

		// Workspace create + list (no workspace context)
		if wsCreateH != nil {
			wsH := handler.NewWorkspaceHandler(wsCreateH, wsGetH, wsListH, memberAddH, memberListH, log)
			// Creating a workspace: rate limit as a write operation.
			r.With(rlWrite).Post("/workspaces", wsH.Create)
			r.With(rlRead).Get("/workspaces", wsH.List)
		}

		// Workspace-scoped routes
		r.Route("/workspaces/{workspaceID}", func(r chi.Router) {
			if wsRepo != nil {
				r.Use(httpmiddleware.WorkspaceAuth(wsRepo))
			}

			wsH := handler.NewWorkspaceHandler(wsCreateH, wsGetH, wsListH, memberAddH, memberListH, log)

			r.With(rlRead).Get("/", wsH.Get)

			r.With(rlWrite,
				httpmiddleware.RequireAction(domainworkspace.ActionManageMembers),
			).Post("/members", wsH.AddMember)
			r.With(rlRead).Get("/members", wsH.ListMembers)

			// URL routes
			r.Route("/urls", func(r chi.Router) {
				if shortenUseCase != nil {
					r.With(rlWrite,
						httpmiddleware.RequireAction(domainworkspace.ActionCreateURL),
					).Post("/", handler.NewShortenHandler(shortenUseCase, log, appMetrics).Handle)
				}
			})

			// API key routes
			if keyCreateH != nil {
				keyH := handler.NewAPIKeyHandler(keyCreateH, keyRevokeH, keyListH, log)
				r.With(rlRead).Get("/api-keys", keyH.List)
				r.With(rlWrite,
					httpmiddleware.RequireAction(domainworkspace.ActionManageMembers),
				).Post("/api-keys", keyH.Create)
				r.With(rlWrite,
					httpmiddleware.RequireAction(domainworkspace.ActionManageMembers),
				).Delete("/api-keys/{keyID}", keyH.Revoke)
			}
		})

		// Legacy route — backwards compat
		if shortenUseCase != nil {
			r.With(rlWrite).Post("/urls",
				handler.NewShortenHandler(shortenUseCase, log, appMetrics).Handle)
		}
	})

	// ── Servers ───────────────────────────────────────────────────────────────
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", appMetrics.Handler())
	metricsSrv := &http.Server{Addr: ":" + cfg.MetricsPort, Handler: metricsMux}

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
			serverErr <- fmt.Errorf("http: %w", err)
		}
	}()
	go func() {
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
	shutdownCtx, cancel := context.WithTimeout(context.Background(),
		time.Duration(cfg.APIShutdownTimeoutS)*time.Second)
	defer cancel()

	_ = srv.Shutdown(shutdownCtx)
	_ = metricsSrv.Shutdown(shutdownCtx)
	if redisClient != nil {
		_ = redisClient.Close()
	}
	if dbClient != nil {
		dbClient.Close()
	}
	_ = otelShutdown(shutdownCtx)
	log.Info("shutdown complete")
}

// noopLimiter always allows requests. Used when Redis is unavailable.
type noopLimiter struct{}

func (n *noopLimiter) Check(_ context.Context, _ string, policy ratelimit.Policy) (*ratelimit.Result, error) {
	return &ratelimit.Result{
		Allowed:   true,
		Remaining: policy.BucketCapacity(),
		Limit:     policy.BucketCapacity(),
		ResetAt:   time.Now().Add(policy.Window),
	}, nil
}

func revokeTokenHandler(dl *infraauth.DenyList, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := domainauth.FromContext(r.Context())
		if !ok {
			response.WriteProblem(w, response.Problem{
				Type: response.ProblemTypeUnauthenticated, Title: "Unauthorized",
				Status: http.StatusUnauthorized,
			})
			return
		}
		if err := dl.Revoke(r.Context(), claims.TokenID, claims.ExpiresAt); err != nil {
			log.Error("revoke failed", slog.String("error", err.Error()))
			response.InternalError(w, r.URL.Path)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func readyHandler(log *slog.Logger, db *postgres.Client, cache *redisinfra.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pingCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if db != nil {
			if err := db.Ping(pingCtx); err != nil {
				log.Warn("readiness: db ping failed")
				response.WriteProblem(w, response.Problem{
					Type: response.ProblemTypeInternal, Title: "Not Ready",
					Status: http.StatusServiceUnavailable, Detail: "database unreachable",
				})
				return
			}
		}
		if cache != nil {
			if err := cache.Ping(pingCtx); err != nil {
				response.WriteProblem(w, response.Problem{
					Type: response.ProblemTypeInternal, Title: "Not Ready",
					Status: http.StatusServiceUnavailable, Detail: "cache unreachable",
				})
				return
			}
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

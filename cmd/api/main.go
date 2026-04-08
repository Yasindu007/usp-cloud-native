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

	log.Info("starting api-service",
		slog.String("build_time", buildTime),
		slog.String("port", cfg.APIPort),
	)

	ctx := context.Background()

	otelShutdown, err := telemetry.InitTracer(ctx, telemetry.Config{
		Enabled: cfg.OTelEnabled, Exporter: cfg.OTelExporter,
		OTLPEndpoint: cfg.OTelEndpoint, ServiceName: cfg.ServiceName,
		ServiceVersion: version, Environment: cfg.Environment,
		SampleRate: cfg.OTelSampleRate,
	})
	if err != nil {
		log.Error("failed to initialize opentelemetry", slog.String("error", err.Error()))
		os.Exit(1)
	}

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
		log.Info("postgresql connected")
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
		log.Info("redis connected")
	}

	// ── JWT auth configuration ─────────────────────────────────────────────────
	var authCfg *httpmiddleware.AuthConfig
	if cfg.JWTPublicKeyPath != "" {
		keySet, err := jwtutil.LoadPublicKeyAsJWKSet(cfg.JWTPublicKeyPath)
		if err != nil {
			log.Error("failed to load JWT public key", slog.String("error", err.Error()))
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
		log.Warn("JWT_PUBLIC_KEY_PATH not set — authentication DISABLED")
		if cfg.IsProduction() {
			os.Exit(1)
		}
	}

	// ── Infrastructure adapters ───────────────────────────────────────────────
	var urlRepo *postgres.URLRepository
	var wsRepo *postgres.WorkspaceRepository

	if dbClient != nil {
		urlRepo = postgres.NewURLRepository(dbClient)
		wsRepo = postgres.NewWorkspaceRepository(dbClient)
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
		wsCreateHandler   *appworkspace.CreateHandler
		wsGetHandler      *appworkspace.GetHandler
		wsListHandler     *appworkspace.ListHandler
		memberAddHandler  *appworkspace.AddMemberHandler
		memberListHandler *appworkspace.ListMembersHandler
	)
	if wsRepo != nil {
		wsCreateHandler = appworkspace.NewCreateHandler(wsRepo, log)
		wsGetHandler = appworkspace.NewGetHandler(wsRepo, wsRepo)
		wsListHandler = appworkspace.NewListHandler(wsRepo, wsRepo)
		memberAddHandler = appworkspace.NewAddMemberHandler(wsRepo, wsRepo, log)
		memberListHandler = appworkspace.NewListMembersHandler(wsRepo)
	}

	// ── HTTP router ───────────────────────────────────────────────────────────
	r := chi.NewRouter()

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

	// ── /api/v1 — authenticated routes ───────────────────────────────────────
	r.Route("/api/v1", func(r chi.Router) {
		// JWT authentication gate
		if authCfg != nil {
			r.Use(httpmiddleware.Authenticate(*authCfg))
		}

		// Token revocation (logout) — auth only, no workspace context needed
		if authCfg != nil && redisClient != nil {
			dl := infraauth.NewDenyList(redisClient.RDB())
			r.Delete("/auth/token", revokeTokenHandler(dl, log))
		}

		// ── Workspace management ──────────────────────────────────────────────
		// POST /workspaces — create a workspace (no workspace context needed,
		//   the user is creating a new one)
		if wsCreateHandler != nil {
			r.Post("/workspaces", handler.NewWorkspaceHandler(
				wsCreateHandler, wsGetHandler, wsListHandler,
				memberAddHandler, memberListHandler, log,
			).Create)
		}

		// GET /workspaces — list workspaces for the current user
		if wsListHandler != nil {
			r.Get("/workspaces", handler.NewWorkspaceHandler(
				wsCreateHandler, wsGetHandler, wsListHandler,
				memberAddHandler, memberListHandler, log,
			).List)
		}

		// ── Workspace-scoped routes: WorkspaceAuth verifies membership ────────
		// All routes under /workspaces/{workspaceID} require the caller to be
		// a member of the specified workspace. WorkspaceAuth enforces this and
		// stores the Member (with role) in context for downstream handlers.
		r.Route("/workspaces/{workspaceID}", func(r chi.Router) {
			if wsRepo != nil {
				r.Use(httpmiddleware.WorkspaceAuth(wsRepo))
			}

			wsHandler := handler.NewWorkspaceHandler(
				wsCreateHandler, wsGetHandler, wsListHandler,
				memberAddHandler, memberListHandler, log,
			)

			// GET /workspaces/{workspaceID} — any member role
			r.Get("/", wsHandler.Get)

			// POST /workspaces/{workspaceID}/members — requires ManageMembers
			r.With(
				httpmiddleware.RequireAction(domainworkspace.ActionManageMembers),
			).Post("/members", wsHandler.AddMember)

			// GET /workspaces/{workspaceID}/members — any member role
			r.Get("/members", wsHandler.ListMembers)

			// ── URL routes scoped to workspace ────────────────────────────────
			r.Route("/urls", func(r chi.Router) {
				// POST — requires write permission (editor, admin, owner)
				if shortenUseCase != nil {
					r.With(
						httpmiddleware.RequireAction(domainworkspace.ActionCreateURL),
					).Post("/",
						handler.NewShortenHandler(shortenUseCase, log, appMetrics).Handle)
				}
				// TODO(story-2.6): GET /, GET /{id}, PATCH /{id}, DELETE /{id}
			})
		})

		// Legacy /urls route (backwards compat during Phase 1 → Phase 2 migration)
		// Will be removed once all clients use /workspaces/{id}/urls
		if shortenUseCase != nil {
			r.Post("/urls",
				handler.NewShortenHandler(shortenUseCase, log, appMetrics).Handle)
		}
	})

	// ── Metrics server ────────────────────────────────────────────────────────
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", appMetrics.Handler())
	metricsSrv := &http.Server{Addr: ":" + cfg.MetricsPort, Handler: metricsMux}

	// ── Application server ────────────────────────────────────────────────────
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
			log.Error("failed to revoke token", slog.String("error", err.Error()))
			response.InternalError(w, r.URL.Path)
			return
		}
		log.Info("token revoked", slog.String("jti", claims.TokenID))
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
			log.Warn("readiness: db ping failed")
			response.WriteProblem(w, response.Problem{
				Type: response.ProblemTypeInternal, Title: "Not Ready",
				Status: http.StatusServiceUnavailable, Detail: "database unreachable",
			})
			return
		}
		if cache != nil {
			if err := cache.Ping(pingCtx); err != nil {
				log.Warn("readiness: redis ping failed")
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

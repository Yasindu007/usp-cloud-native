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
	appkey "github.com/urlshortener/platform/internal/application/apikey"
	appaudit "github.com/urlshortener/platform/internal/application/audit"
	appexport "github.com/urlshortener/platform/internal/application/export"
	"github.com/urlshortener/platform/internal/application/shorten"
	appurl "github.com/urlshortener/platform/internal/application/url"
	appwebhook "github.com/urlshortener/platform/internal/application/webhook"
	appworkspace "github.com/urlshortener/platform/internal/application/workspace"
	"github.com/urlshortener/platform/internal/config"
	infraauth "github.com/urlshortener/platform/internal/infrastructure/auth"
	"github.com/urlshortener/platform/internal/infrastructure/metrics"
	"github.com/urlshortener/platform/internal/infrastructure/postgres"
	redisinfra "github.com/urlshortener/platform/internal/infrastructure/redis"
	"github.com/urlshortener/platform/internal/infrastructure/storage"
	"github.com/urlshortener/platform/internal/interfaces/http/handler"
	httpmiddleware "github.com/urlshortener/platform/internal/interfaces/http/middleware"
	"github.com/urlshortener/platform/internal/interfaces/http/response"
	"github.com/urlshortener/platform/pkg/jwtutil"
	"github.com/urlshortener/platform/pkg/logger"
	"github.com/urlshortener/platform/pkg/shortcode"
	"github.com/urlshortener/platform/pkg/signedurl"
	"github.com/urlshortener/platform/pkg/telemetry"

	domainaudit "github.com/urlshortener/platform/internal/domain/audit"
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
		Enabled: cfg.OTelEnabled, Exporter: cfg.OTelExporter,
		OTLPEndpoint: cfg.OTelEndpoint, ServiceName: cfg.ServiceName,
		ServiceVersion: version, Environment: cfg.Environment,
		SampleRate: cfg.OTelSampleRate,
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

	var tokenBucketLimiter *redisinfra.TokenBucketLimiter
	if redisClient != nil {
		tokenBucketLimiter = redisinfra.NewTokenBucketLimiter(redisClient)
	}

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
	var auditRepo *postgres.AuditRepository
	var analyticsQueryRepo *postgres.AnalyticsQueryRepository
	var exportRepo *postgres.ExportRepository
	var webhookRepo *postgres.WebhookRepository

	if dbClient != nil {
		urlRepo = postgres.NewURLRepository(dbClient)
		wsRepo = postgres.NewWorkspaceRepository(dbClient)
		keyRepo = postgres.NewAPIKeyRepository(dbClient)
		auditRepo = postgres.NewAuditRepository(dbClient)
		analyticsQueryRepo = postgres.NewAnalyticsQueryRepository(dbClient)
		exportRepo = postgres.NewExportRepository(dbClient)
		webhookRepo = postgres.NewWebhookRepository(dbClient)
	}

	var urlCache *redisinfra.URLCache
	var clickSubscriber *redisinfra.ClickSubscriber
	if redisClient != nil {
		urlCache = redisinfra.NewURLCache(redisClient)
		clickSubscriber = redisinfra.NewClickSubscriber(redisClient)
	}

	exportStorage, err := storage.NewLocalStorage(cfg.ExportStorageDir)
	if err != nil {
		log.Error("export storage init failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
	var exportSigner *signedurl.Signer
	if cfg.ExportSignSecret != "" {
		exportSigner, err = signedurl.NewFromHex(cfg.ExportSignSecret)
	} else {
		exportSigner, err = signedurl.NewRandom()
	}
	if err != nil {
		log.Error("export signer init failed", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// ── Audit service ─────────────────────────────────────────────────────────
	auditCtx, auditCancel := context.WithCancel(ctx)
	var auditSvc *appaudit.Service
	if auditRepo != nil {
		auditSvc = appaudit.NewService(auditCtx, auditRepo, log)
		log.Info("audit service started")
	}

	// ── Application layer ─────────────────────────────────────────────────────
	codeGenerator := shortcode.New(cfg.ShortCodeLength)

	var shortenUseCase *shorten.Handler
	var urlGetH *appurl.GetHandler
	var urlListH *appurl.ListHandler
	var urlUpdateH *appurl.UpdateHandler
	var urlDeleteH *appurl.DeleteHandler

	// Analytics use cases
	var analyticsSummaryH *appanalytics.SummaryHandler
	var analyticsTimeSeriesH *appanalytics.TimeSeriesHandler
	var analyticsBreakdownH *appanalytics.BreakdownHandler
	var exportCreateH *appexport.CreateHandler
	var webhookRegisterH *appwebhook.RegisterHandler
	var webhookListH *appwebhook.ListHandler
	var webhookDeleteH *appwebhook.DeleteHandler
	var webhookDispatcher *appwebhook.Dispatcher

	if urlRepo != nil {
		shortenUseCase = shorten.NewHandler(
			urlRepo, urlCache, codeGenerator,
			cfg.BaseURL, cfg.RedirectCacheTTLS, log,
		)
		urlGetH = appurl.NewGetHandler(urlRepo, cfg.BaseURL)
		urlListH = appurl.NewListHandler(urlRepo, cfg.BaseURL)
		urlUpdateH = appurl.NewUpdateHandler(urlRepo, urlCache, cfg.BaseURL, log)
		urlDeleteH = appurl.NewDeleteHandler(urlRepo, urlCache, log)
	}

	if analyticsQueryRepo != nil && urlRepo != nil {
		analyticsSummaryH = appanalytics.NewSummaryHandler(analyticsQueryRepo, urlRepo, cfg.BaseURL)
		analyticsTimeSeriesH = appanalytics.NewTimeSeriesHandler(analyticsQueryRepo, urlRepo)
		analyticsBreakdownH = appanalytics.NewBreakdownHandler(analyticsQueryRepo, urlRepo)
		log.Info("analytics query handlers enabled")
	}
	if exportRepo != nil {
		exportCreateH = appexport.NewCreateHandler(exportRepo, log, cfg.ExportMaxWindowDays)
	}
	if webhookRepo != nil {
		webhookRegisterH = appwebhook.NewRegisterHandler(webhookRepo, log)
		webhookListH = appwebhook.NewListHandler(webhookRepo)
		webhookDeleteH = appwebhook.NewDeleteHandler(webhookRepo)
		webhookDispatcher = appwebhook.NewDispatcher(webhookRepo, webhookRepo, log)
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

	// ── Middleware ─────────────────────────────────────────────────────────────
	var effectiveLimiter httpmiddleware.Limiter
	if tokenBucketLimiter != nil {
		effectiveLimiter = tokenBucketLimiter
	} else {
		effectiveLimiter = &noopLimiter{}
	}

	rlRead := httpmiddleware.RateLimit(httpmiddleware.RateLimitConfig{
		Limiter: effectiveLimiter, ServiceName: cfg.ServiceName,
		EndpointClass: ratelimit.ClassRead, Log: log, FailOpen: true,
	})
	rlWrite := httpmiddleware.RateLimit(httpmiddleware.RateLimitConfig{
		Limiter: effectiveLimiter, ServiceName: cfg.ServiceName,
		EndpointClass: ratelimit.ClassWrite, Log: log, FailOpen: true,
	})

	auditOf := func(action domainaudit.Action) func(http.Handler) http.Handler {
		if auditSvc == nil {
			return func(next http.Handler) http.Handler { return next }
		}
		return httpmiddleware.AuditAction(auditSvc, action)
	}

	// ── Router ────────────────────────────────────────────────────────────────
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
	r.Get("/api/v1/healthz", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, map[string]string{"status": "alive"})
	})
	r.Get("/readyz", readyHandler(log, dbClient, redisClient))
	if shortenUseCase != nil {
		r.With(rlWrite, auditOf(domainaudit.ActionURLCreate)).
			Post("/api/v1/workspaces/{workspaceID}/urls", handler.NewShortenHandler(shortenUseCase, log, appMetrics).
				WithWebhookDispatcher(webhookDispatcher).Handle)
	}
	if exportRepo != nil {
		exportH := handler.NewExportHandler(nil, nil, exportRepo, exportStorage, exportSigner, cfg.APIBaseURL, log)
		r.Get("/api/v1/exports/{exportID}/download", exportH.Download)
	}

	r.Route("/api/v1", func(r chi.Router) {
		if keyRepo != nil {
			r.Use(httpmiddleware.APIKeyAuth(keyRepo, log))
		}
		if authCfg != nil {
			r.Use(httpmiddleware.Authenticate(*authCfg))
		}

		if authCfg != nil && redisClient != nil {
			dl := infraauth.NewDenyList(redisClient.RDB())
			r.With(auditOf(domainaudit.ActionTokenRevoke)).
				Delete("/auth/token", revokeTokenHandler(dl, log))
		}

		if wsCreateH != nil {
			wsH := handler.NewWorkspaceHandler(wsCreateH, wsGetH, wsListH, memberAddH, memberListH, log)
			r.With(rlWrite, auditOf(domainaudit.ActionWorkspaceCreate)).
				Post("/workspaces", wsH.Create)
			r.With(rlRead).Get("/workspaces", wsH.List)
		}

		r.Route("/workspaces/{workspaceID}", func(r chi.Router) {
			if wsRepo != nil {
				r.Use(httpmiddleware.WorkspaceAuth(wsRepo))
			}

			wsH := handler.NewWorkspaceHandler(wsCreateH, wsGetH, wsListH, memberAddH, memberListH, log)
			r.With(rlRead).Get("/", wsH.Get)
			r.With(rlWrite,
				httpmiddleware.RequireAction(domainworkspace.ActionManageMembers),
				auditOf(domainaudit.ActionMemberAdd),
			).Post("/members", wsH.AddMember)
			r.With(rlRead).Get("/members", wsH.ListMembers)

			if clickSubscriber != nil && urlRepo != nil {
				streamH := handler.NewStreamHandler(urlRepo, clickSubscriber, log)
				r.With(rlRead, httpmiddleware.RequireAction(domainworkspace.ActionViewAnalytics)).
					Get("/stream", streamH.StreamWorkspace)
			}
			if exportRepo != nil && exportCreateH != nil {
				exportH := handler.NewExportHandler(exportCreateH, exportRepo, exportRepo, exportStorage, exportSigner, cfg.APIBaseURL, log)
				r.With(rlRead, httpmiddleware.RequireAction(domainworkspace.ActionViewAnalytics)).
					Get("/exports", exportH.List)
				r.With(rlRead, httpmiddleware.RequireAction(domainworkspace.ActionViewAnalytics)).
					Post("/exports", exportH.Create)
				r.With(rlRead, httpmiddleware.RequireAction(domainworkspace.ActionViewAnalytics)).
					Get("/exports/{exportID}", exportH.Get)
			}
			if webhookRegisterH != nil {
				webhookH := handler.NewWebhookHandler(webhookRegisterH, webhookListH, webhookDeleteH, log)
				r.With(rlRead, httpmiddleware.RequireAction(domainworkspace.ActionManageWebhooks)).
					Get("/webhooks", webhookH.List)
				r.With(rlWrite,
					httpmiddleware.RequireAction(domainworkspace.ActionManageWebhooks),
					auditOf(domainaudit.ActionWebhookCreate),
				).Post("/webhooks", webhookH.Register)
				r.With(rlWrite,
					httpmiddleware.RequireAction(domainworkspace.ActionManageWebhooks),
					auditOf(domainaudit.ActionWebhookDelete),
				).Delete("/webhooks/{webhookID}", webhookH.Delete)
			}

			r.Route("/urls", func(r chi.Router) {
				urlH := handler.NewURLHandler(urlGetH, urlListH, urlUpdateH, urlDeleteH, log).
					WithWebhookDispatcher(webhookDispatcher)

				if shortenUseCase != nil {
					r.With(rlWrite,
						httpmiddleware.RequireAction(domainworkspace.ActionCreateURL),
						auditOf(domainaudit.ActionURLCreate),
					).Post("/", handler.NewShortenHandler(shortenUseCase, log, appMetrics).
						WithWebhookDispatcher(webhookDispatcher).Handle)
				}
				if urlListH != nil {
					r.With(rlRead, httpmiddleware.RequireAction(domainworkspace.ActionViewURL)).
						Get("/", urlH.List)
				}

				r.Route("/{urlID}", func(r chi.Router) {
					if urlGetH != nil {
						r.With(rlRead, httpmiddleware.RequireAction(domainworkspace.ActionViewURL)).
							Get("/", urlH.Get)
					}
					if urlUpdateH != nil {
						r.With(rlWrite,
							httpmiddleware.RequireAction(domainworkspace.ActionUpdateURL),
							auditOf(domainaudit.ActionURLUpdate),
						).Patch("/", urlH.Update)
					}
					if urlDeleteH != nil {
						r.With(rlWrite,
							httpmiddleware.RequireAction(domainworkspace.ActionDeleteURL),
							auditOf(domainaudit.ActionURLDelete),
						).Delete("/", urlH.Delete)
					}

					// ── Analytics sub-routes ────────────────────────────────
					// Analytics endpoints are read-only — any workspace member
					// with ViewURL permission can access them.
					// Rate limited as reads (not writes).
					if analyticsSummaryH != nil {
						analyticsH := handler.NewAnalyticsHandler(
							analyticsSummaryH, analyticsTimeSeriesH, analyticsBreakdownH, log,
						)
						r.With(rlRead, httpmiddleware.RequireAction(domainworkspace.ActionViewAnalytics)).
							Get("/analytics", analyticsH.GetSummary)
						r.With(rlRead, httpmiddleware.RequireAction(domainworkspace.ActionViewAnalytics)).
							Get("/analytics/timeseries", analyticsH.GetTimeSeries)
						r.With(rlRead, httpmiddleware.RequireAction(domainworkspace.ActionViewAnalytics)).
							Get("/analytics/breakdown", analyticsH.GetBreakdown)
					}
					if clickSubscriber != nil && urlRepo != nil {
						streamH := handler.NewStreamHandler(urlRepo, clickSubscriber, log)
						r.With(rlRead, httpmiddleware.RequireAction(domainworkspace.ActionViewAnalytics)).
							Get("/stream", streamH.StreamURL)
					}
				})
			})

			if keyCreateH != nil {
				keyH := handler.NewAPIKeyHandler(keyCreateH, keyRevokeH, keyListH, log)
				r.With(rlRead).Get("/api-keys", keyH.List)
				r.With(rlWrite,
					httpmiddleware.RequireAction(domainworkspace.ActionManageMembers),
					auditOf(domainaudit.ActionAPIKeyCreate),
				).Post("/api-keys", keyH.Create)
				r.With(rlWrite,
					httpmiddleware.RequireAction(domainworkspace.ActionManageMembers),
					auditOf(domainaudit.ActionAPIKeyRevoke),
				).Delete("/api-keys/{keyID}", keyH.Revoke)
			}
		})

		if shortenUseCase != nil {
			r.With(rlWrite, auditOf(domainaudit.ActionURLCreate)).
				Post("/urls", handler.NewShortenHandler(shortenUseCase, log, appMetrics).
					WithWebhookDispatcher(webhookDispatcher).Handle)
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

	var exportWorkerCancel context.CancelFunc
	if exportRepo != nil {
		exportWorkerCtx, cancel := context.WithCancel(ctx)
		exportWorkerCancel = cancel
		go appexport.NewWorker(
			exportRepo,
			exportRepo,
			exportStorage,
			exportSigner,
			log,
			time.Duration(cfg.ExportDownloadTTLH)*time.Hour,
			time.Duration(cfg.ExportWorkerPollS)*time.Second,
		).Run(exportWorkerCtx)
	}
	var webhookWorkerCancel context.CancelFunc
	if webhookRepo != nil && cfg.WebhookWorkerEnabled {
		webhookWorkerCtx, cancel := context.WithCancel(ctx)
		webhookWorkerCancel = cancel
		go appwebhook.NewWorker(webhookRepo, webhookRepo, appwebhook.WorkerConfig{
			BatchSize:    cfg.WebhookWorkerBatchSize,
			PollInterval: time.Duration(cfg.WebhookWorkerPollIntervalS) * time.Second,
			HTTPTimeout:  time.Duration(cfg.WebhookWorkerHTTPTimeoutS) * time.Second,
		}, log).Run(webhookWorkerCtx)
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-serverErr:
		log.Error("server error", slog.String("error", err.Error()))
	case sig := <-quit:
		log.Info("shutdown signal received", slog.String("signal", sig.String()))
	}

	statsCancel()
	if exportWorkerCancel != nil {
		exportWorkerCancel()
	}
	if webhookWorkerCancel != nil {
		webhookWorkerCancel()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(),
		time.Duration(cfg.APIShutdownTimeoutS)*time.Second)
	defer cancel()

	_ = srv.Shutdown(shutdownCtx)
	_ = metricsSrv.Shutdown(shutdownCtx)

	if auditSvc != nil {
		auditCancel()
		auditSvc.Shutdown(shutdownCtx)
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
		domainaudit.AnnotateContext(r.Context(), domainaudit.ResourceToken,
			claims.TokenID, map[string]any{"revoked_by": claims.UserID})
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

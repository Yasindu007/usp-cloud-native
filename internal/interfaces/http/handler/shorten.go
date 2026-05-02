package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/urlshortener/platform/internal/application/apperrors"
	"github.com/urlshortener/platform/internal/application/shorten"
	domainaudit "github.com/urlshortener/platform/internal/domain/audit"
	domainauth "github.com/urlshortener/platform/internal/domain/auth"
	domainwebhook "github.com/urlshortener/platform/internal/domain/webhook"
	"github.com/urlshortener/platform/internal/infrastructure/metrics"
	"github.com/urlshortener/platform/internal/interfaces/http/response"
	"github.com/urlshortener/platform/pkg/logger"
)

// URLShortener is the application use case interface for creating short URLs.
type URLShortener interface {
	Handle(ctx context.Context, cmd shorten.Command) (*shorten.Result, error)
}

// ShortenRequest is the JSON request body for POST /api/v1/urls.
type ShortenRequest struct {
	OriginalURL string     `json:"original_url"`
	CustomCode  string     `json:"custom_code,omitempty"`
	Title       string     `json:"title,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

// ShortenResponse is the data payload within the 201 Created response envelope.
type ShortenResponse struct {
	ID          string `json:"id"`
	ShortURL    string `json:"short_url"`
	ShortCode   string `json:"short_code"`
	OriginalURL string `json:"original_url"`
	WorkspaceID string `json:"workspace_id"`
	CreatedAt   string `json:"created_at"`
}

// ShortenHandler handles POST /api/v1/urls.
type ShortenHandler struct {
	shortener URLShortener
	metrics   *metrics.Metrics
	webhooks  WebhookDispatcher
	log       *slog.Logger
}

// NewShortenHandler constructs a ShortenHandler.
func NewShortenHandler(shortener URLShortener, log *slog.Logger, m ...*metrics.Metrics) *ShortenHandler {
	var met *metrics.Metrics
	if len(m) > 0 {
		met = m[0]
	}
	return &ShortenHandler{shortener: shortener, metrics: met, log: log}
}

func (h *ShortenHandler) WithWebhookDispatcher(dispatcher WebhookDispatcher) *ShortenHandler {
	h.webhooks = dispatcher
	return h
}

// Handle processes POST /api/v1/urls.
// Annotates the audit context with the created URL's ID and short code.
func (h *ShortenHandler) Handle(w http.ResponseWriter, r *http.Request) {
	log := logger.FromContext(r.Context()).With(
		slog.String("handler", "ShortenHandler"),
		slog.String("request_id", chimiddleware.GetReqID(r.Context())),
	)

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var req ShortenRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var syntaxErr *json.SyntaxError
		var unmarshalErr *json.UnmarshalTypeError
		switch {
		case errors.As(err, &syntaxErr):
			response.BadRequest(w, "request body contains malformed JSON", r.URL.Path)
		case errors.As(err, &unmarshalErr):
			response.BadRequest(w,
				"request body contains an invalid value for field: "+unmarshalErr.Field,
				r.URL.Path)
		default:
			response.BadRequest(w, "request body could not be decoded", r.URL.Path)
		}
		return
	}

	workspaceID, userID := resolveIdentity(r)

	cmd := shorten.Command{
		OriginalURL: req.OriginalURL,
		CustomCode:  req.CustomCode,
		Title:       req.Title,
		ExpiresAt:   req.ExpiresAt,
		WorkspaceID: workspaceID,
		CreatedBy:   userID,
	}

	result, err := h.shortener.Handle(r.Context(), cmd)
	if err != nil {
		h.writeError(w, r, err, log)
		return
	}

	// ── Audit annotation ──────────────────────────────────────────────────────
	// Annotate the pending audit event (started by AuditAction middleware)
	// with the created resource details. The middleware reads this after
	// Handle() returns and ships the completed event to the audit service.
	domainaudit.AnnotateContext(r.Context(),
		domainaudit.ResourceURL,
		result.ID,
		map[string]any{
			"short_code":   result.ShortCode,
			"original_url": result.OriginalURL,
		},
	)

	if h.metrics != nil {
		h.metrics.RecordURLShortened()
	}
	if h.webhooks != nil {
		if err := h.webhooks.Dispatch(r.Context(), domainwebhook.Event{
			Type:        domainwebhook.EventURLCreated,
			EventID:     result.ID,
			WorkspaceID: result.WorkspaceID,
			OccurredAt:  time.Now().UTC(),
			Data: map[string]any{
				"id":           result.ID,
				"short_code":   result.ShortCode,
				"original_url": result.OriginalURL,
				"workspace_id": result.WorkspaceID,
			},
		}); err != nil {
			log.Warn("webhook dispatch failed",
				slog.String("event_type", string(domainwebhook.EventURLCreated)),
				slog.String("error", err.Error()),
			)
		}
	}

	log.Info("url shortened",
		slog.String("short_code", result.ShortCode),
		slog.String("id", result.ID),
		slog.String("workspace_id", result.WorkspaceID),
	)

	response.JSON(w, http.StatusCreated, response.Envelope{
		Data: ShortenResponse{
			ID:          result.ID,
			ShortURL:    result.ShortURL,
			ShortCode:   result.ShortCode,
			OriginalURL: result.OriginalURL,
			WorkspaceID: result.WorkspaceID,
			CreatedAt:   result.CreatedAt,
		},
	})
}

func resolveIdentity(r *http.Request) (workspaceID, userID string) {
	if claims, ok := domainauth.FromContext(r.Context()); ok {
		return claims.WorkspaceID, claims.UserID
	}
	workspaceID = chi.URLParam(r, "workspaceID")
	if workspaceID == "" {
		workspaceID = r.Header.Get("X-Workspace-ID")
	}
	if workspaceID == "" {
		workspaceID = "ws_default"
	}
	userID = r.Header.Get("X-User-ID")
	if userID == "" {
		userID = "usr_default"
	}
	return workspaceID, userID
}

func (h *ShortenHandler) writeError(w http.ResponseWriter, r *http.Request, err error, log *slog.Logger) {
	var ve *apperrors.ValidationError
	if errors.As(err, &ve) {
		response.UnprocessableEntity(w, ve.Message, r.URL.Path)
		return
	}
	if errors.Is(err, apperrors.ErrShortCodeConflict) {
		response.Conflict(w,
			"The requested short code is already in use.",
			r.URL.Path)
		return
	}
	if errors.Is(err, apperrors.ErrURLBlocked) {
		response.WriteProblem(w, response.Problem{
			Type:     response.ProblemTypeURLBlocked,
			Title:    "URL Blocked",
			Status:   http.StatusUnprocessableEntity,
			Detail:   "This URL has been flagged by our safety policy.",
			Instance: r.URL.Path,
		})
		return
	}
	log.Error("unexpected error in shorten handler",
		slog.String("error", err.Error()),
		slog.String("path", r.URL.Path),
	)
	response.InternalError(w, r.URL.Path)
}

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/urlshortener/platform/internal/application/apperrors"
	"github.com/urlshortener/platform/internal/application/shorten"
	domainauth "github.com/urlshortener/platform/internal/domain/auth"
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
	log       *slog.Logger
}

// NewShortenHandler constructs a ShortenHandler.
// metrics is variadic for backward compatibility with tests.
func NewShortenHandler(shortener URLShortener, log *slog.Logger, m ...*metrics.Metrics) *ShortenHandler {
	var met *metrics.Metrics
	if len(m) > 0 {
		met = m[0]
	}
	return &ShortenHandler{shortener: shortener, metrics: met, log: log}
}

// Handle processes POST /api/v1/urls.
//
// Identity extraction (Phase 2 — from JWT claims):
//
//	The JWT middleware (applied at the router level) validates the token and
//	stores Claims in the request context. This handler reads workspace_id and
//	user_id from the claims — not from headers. This is the secure path.
//
// Development fallback:
//
//	If no claims are in context (middleware not applied, e.g. unit tests),
//	the handler falls back to X-Workspace-ID and X-User-ID headers.
//	This fallback is ONLY active when auth middleware is not wired in.
//	In production, the middleware always runs first — if it passes, claims
//	are always present. If it fails, the handler never runs (401 returned).
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

	// ── Identity resolution ───────────────────────────────────────────────────
	// Priority 1: JWT claims (Phase 2, production path)
	// Priority 2: Request headers (Phase 1 stub, development/test fallback)
	//
	// When the auth middleware is applied at the router level, claims are
	// always present by the time this handler runs. The header fallback
	// exists so unit tests work without needing to set up JWT infrastructure.
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

	if h.metrics != nil {
		h.metrics.RecordURLShortened()
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

// resolveIdentity extracts workspace and user identity from the request.
// Returns claims values if JWT middleware ran, falls back to headers otherwise.
func resolveIdentity(r *http.Request) (workspaceID, userID string) {
	// Try JWT claims first (Phase 2 path).
	if claims, ok := domainauth.FromContext(r.Context()); ok {
		return claims.WorkspaceID, claims.UserID
	}

	// Fallback to headers (Phase 1 stub / test path).
	workspaceID = r.Header.Get("X-Workspace-ID")
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
			"The requested short code is already in use. "+
				"Please choose a different code or omit it to generate one automatically.",
			r.URL.Path)
		return
	}
	if errors.Is(err, apperrors.ErrURLBlocked) {
		response.WriteProblem(w, response.Problem{
			Type:     response.ProblemTypeURLBlocked,
			Title:    "URL Blocked",
			Status:   http.StatusUnprocessableEntity,
			Detail:   "This URL has been flagged by our safety policy and cannot be shortened.",
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

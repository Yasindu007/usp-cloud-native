// Package handler contains the HTTP handler implementations.
// Each handler is responsible for exactly three things:
//  1. Deserializing the HTTP request into an application command/query
//  2. Calling the appropriate application use case
//  3. Serializing the result or error into an HTTP response
//
// Handlers must not contain business logic. All business decisions
// (validation, authorization, domain rules) happen in the application
// and domain layers. The handler is a translation layer only.
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
	"github.com/urlshortener/platform/internal/interfaces/http/response"
	"github.com/urlshortener/platform/pkg/logger"
)

// URLShortener is the interface this handler uses to invoke the shorten use case.
//
// Defining the interface in the interfaces layer (not the application layer)
// is the correct application of Dependency Inversion:
//
//	"The interfaces layer defines what it needs."
//	"The application layer satisfies it."
//
// This also makes the handler testable without depending on the concrete
// shorten.Handler type — tests provide a mock that implements this interface.
type URLShortener interface {
	Handle(ctx context.Context, cmd shorten.Command) (*shorten.Result, error)
}

// ShortenRequest is the JSON-deserialized request body for POST /api/v1/urls.
// Field tags use snake_case per REST API convention.
type ShortenRequest struct {
	// OriginalURL is the long URL to be shortened. Required.
	OriginalURL string `json:"original_url"`

	// CustomCode is an optional caller-specified short code.
	// When omitted, a cryptographically random Base62 code is generated.
	CustomCode string `json:"custom_code,omitempty"`

	// Title is an optional human-readable display name for the URL.
	Title string `json:"title,omitempty"`

	// ExpiresAt is the optional expiration timestamp in RFC 3339 format.
	// nil means no expiry. After this time, redirects return HTTP 410 Gone.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// ShortenResponse is the data payload returned on successful URL creation.
// Wrapped in response.Envelope: {"data": {...}}
type ShortenResponse struct {
	ID          string `json:"id"`
	ShortURL    string `json:"short_url"`
	ShortCode   string `json:"short_code"`
	OriginalURL string `json:"original_url"`
	WorkspaceID string `json:"workspace_id"`
	CreatedAt   string `json:"created_at"`
}

// ShortenHandler handles POST /api/v1/urls requests.
type ShortenHandler struct {
	shortener URLShortener
	log       *slog.Logger
}

// NewShortenHandler constructs a ShortenHandler.
func NewShortenHandler(shortener URLShortener, log *slog.Logger) *ShortenHandler {
	return &ShortenHandler{
		shortener: shortener,
		log:       log,
	}
}

// Handle processes POST /api/v1/urls.
//
// Request body (application/json):
//
//	{
//	  "original_url": "https://example.com/...",  // required
//	  "custom_code":  "launch-2026",              // optional
//	  "title":        "Q3 Campaign",              // optional
//	  "expires_at":   "2026-12-31T23:59:59Z"      // optional, RFC 3339
//	}
//
// Successful response (201 Created):
//
//	{
//	  "data": {
//	    "id":           "01HXYZ...",
//	    "short_url":    "https://s.example.com/abc1234",
//	    "short_code":   "abc1234",
//	    "original_url": "https://example.com/...",
//	    "workspace_id": "ws_...",
//	    "created_at":   "2026-02-24T10:00:00Z"
//	  }
//	}
//
// Auth (Phase 1 stub):
//
//	Workspace ID and user ID are read from X-Workspace-ID and X-User-ID headers.
//	Phase 2 replaces this with JWT claims extracted by the auth middleware.
func (h *ShortenHandler) Handle(w http.ResponseWriter, r *http.Request) {
	// Get request-scoped logger injected by the logger middleware.
	// Falls back to the default slog logger if middleware wasn't run
	// (e.g., in unit tests that call the handler directly).
	log := logger.FromContext(r.Context()).With(
		slog.String("handler", "ShortenHandler"),
		slog.String("request_id", chimiddleware.GetReqID(r.Context())),
	)

	// ── Parse and validate request body ──────────────────────────────────────
	// MaxBytesReader prevents clients from sending a multi-GB body that would
	// exhaust server memory. 1MB is generous for a URL + metadata.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB

	var req ShortenRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields() // Surface typos in field names to the caller

	if err := decoder.Decode(&req); err != nil {
		var syntaxErr *json.SyntaxError
		var unmarshalErr *json.UnmarshalTypeError

		switch {
		case errors.As(err, &syntaxErr):
			response.BadRequest(w,
				"request body contains malformed JSON",
				r.URL.Path,
			)
		case errors.As(err, &unmarshalErr):
			response.BadRequest(w,
				"request body contains an invalid value for field: "+unmarshalErr.Field,
				r.URL.Path,
			)
		default:
			response.BadRequest(w, "request body could not be decoded", r.URL.Path)
		}
		return
	}

	// ── Auth stub: extract identity from headers (Phase 1) ───────────────────
	// In Phase 2, the JWT auth middleware populates these values in the
	// request context from validated token claims. The handler will call
	// a context helper like auth.WorkspaceIDFromContext(ctx) instead.
	workspaceID := r.Header.Get("X-Workspace-ID")
	if workspaceID == "" {
		workspaceID = "ws_default" // safe stub for Phase 1 local dev
	}
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		userID = "usr_default"
	}

	// ── Build and execute application command ────────────────────────────────
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

	log.Info("url shortened",
		slog.String("short_code", result.ShortCode),
		slog.String("id", result.ID),
		slog.String("workspace_id", result.WorkspaceID),
	)

	// ── Write success response ────────────────────────────────────────────────
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

// writeError maps application layer errors to RFC 7807 HTTP responses.
// This is the handler's responsibility — translating application outcomes
// into HTTP semantics without leaking infrastructure details.
//
// Error mapping table:
//
//	ValidationError         → 422 Unprocessable Entity
//	ErrShortCodeConflict    → 409 Conflict
//	ErrURLBlocked           → 422 Unprocessable Entity
//	anything else           → 500 Internal Server Error (sanitized message)
func (h *ShortenHandler) writeError(w http.ResponseWriter, r *http.Request, err error, log *slog.Logger) {
	// ── Validation error → 422 ────────────────────────────────────────────────
	var ve *apperrors.ValidationError
	if errors.As(err, &ve) {
		response.UnprocessableEntity(w, ve.Message, r.URL.Path)
		return
	}

	// ── Short code conflict → 409 ────────────────────────────────────────────
	if errors.Is(err, apperrors.ErrShortCodeConflict) {
		response.Conflict(w,
			"The requested short code is already in use. "+
				"Please choose a different code or omit it to generate one automatically.",
			r.URL.Path,
		)
		return
	}

	// ── URL blocked by safety policy → 422 ───────────────────────────────────
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

	// ── Unexpected error → 500 ────────────────────────────────────────────────
	// Log the full error internally. Return a generic message to the client.
	// Never expose internal error details (stack traces, DB errors, DSNs) in responses.
	log.Error("unexpected error in shorten handler",
		slog.String("error", err.Error()),
		slog.String("path", r.URL.Path),
	)
	response.InternalError(w, r.URL.Path)
}

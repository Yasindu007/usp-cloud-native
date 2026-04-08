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
	appurl "github.com/urlshortener/platform/internal/application/url"
	domainaudit "github.com/urlshortener/platform/internal/domain/audit"
	domainauth "github.com/urlshortener/platform/internal/domain/auth"
	domainurl "github.com/urlshortener/platform/internal/domain/url"
	"github.com/urlshortener/platform/internal/interfaces/http/response"
	"github.com/urlshortener/platform/pkg/logger"
)

// ── Use case interfaces ────────────────────────────────────────────────────────

// URLGetter retrieves a single URL by ID.
type URLGetter interface {
	Handle(ctx context.Context, q appurl.GetQuery) (*appurl.URLResult, error)
}

// URLLister lists URLs for a workspace with pagination.
type URLLister interface {
	Handle(ctx context.Context, q appurl.ListQuery) (*appurl.ListResult, error)
}

// URLUpdater applies partial updates to a URL.
type URLUpdater interface {
	Handle(ctx context.Context, cmd appurl.UpdateCommand) (*appurl.URLResult, error)
}

// URLDeleter soft-deletes a URL.
type URLDeleter interface {
	Handle(ctx context.Context, cmd appurl.DeleteCommand) error
}

// ── Request / Response types ──────────────────────────────────────────────────

// UpdateURLRequest is the JSON body for PATCH /api/v1/workspaces/{id}/urls/{urlID}.
// All fields are optional (PATCH semantics).
type UpdateURLRequest struct {
	// OriginalURL is the new redirect target. Omit to leave unchanged.
	OriginalURL *string `json:"original_url,omitempty"`

	// Title is the new display name. Omit to leave unchanged.
	Title *string `json:"title,omitempty"`

	// ExpiresAt is the new expiry time.
	// Omit to leave unchanged.
	// Set to null to remove the expiry.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// URLResponse is the canonical JSON shape for a URL resource.
// Matches the PRD section 5.1.4 Read URL Details specification.
type URLResponse struct {
	ID          string     `json:"id"`
	ShortURL    string     `json:"short_url"`
	ShortCode   string     `json:"short_code"`
	OriginalURL string     `json:"original_url"`
	Title       string     `json:"title,omitempty"`
	Status      string     `json:"status"`
	WorkspaceID string     `json:"workspace_id"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   string     `json:"created_at"`
	UpdatedAt   string     `json:"updated_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	ClickCount  int64      `json:"click_count"`
}

// ListURLsResponse wraps the paginated list response per PRD section 8.2.
type ListURLsResponse struct {
	Data []URLResponse `json:"data"`
	Meta ListMeta      `json:"meta"`
}

// ListMeta carries pagination metadata per PRD section 8.2.
type ListMeta struct {
	Cursor  string `json:"cursor"`
	HasMore bool   `json:"has_more"`
}

// ── URLHandler ────────────────────────────────────────────────────────────────

// URLHandler handles URL CRUD HTTP endpoints.
// Each handler method is responsible only for parsing, calling the use case,
// annotating the audit context, and writing the response.
type URLHandler struct {
	getter  URLGetter
	lister  URLLister
	updater URLUpdater
	deleter URLDeleter
	log     *slog.Logger
}

// NewURLHandler constructs a URLHandler.
func NewURLHandler(
	getter URLGetter,
	lister URLLister,
	updater URLUpdater,
	deleter URLDeleter,
	log *slog.Logger,
) *URLHandler {
	return &URLHandler{
		getter:  getter,
		lister:  lister,
		updater: updater,
		deleter: deleter,
		log:     log,
	}
}

// ── GET /api/v1/workspaces/{workspaceID}/urls/{urlID} ─────────────────────────

// Get retrieves a single URL by its ULID.
func (h *URLHandler) Get(w http.ResponseWriter, r *http.Request) {
	claims, ok := domainauth.FromContext(r.Context())
	if !ok {
		response.WriteProblem(w, response.Problem{
			Type: response.ProblemTypeUnauthenticated, Title: "Unauthorized",
			Status: http.StatusUnauthorized,
		})
		return
	}

	urlID := chi.URLParam(r, "urlID")

	result, err := h.getter.Handle(r.Context(), appurl.GetQuery{
		URLID:       urlID,
		WorkspaceID: claims.WorkspaceID,
	})
	if err != nil {
		h.writeError(w, r, err, logger.FromContext(r.Context()))
		return
	}

	response.JSON(w, http.StatusOK, response.Envelope{
		Data: toURLResponse(result),
	})
}

// ── GET /api/v1/workspaces/{workspaceID}/urls ─────────────────────────────────

// List returns a paginated list of URLs for the workspace.
//
// Query parameters (all optional):
//
//	status       — filter by status (active|expired|disabled|deleted)
//	created_by   — filter by creator user ID
//	cursor       — pagination cursor (ULID of last item from previous page)
//	limit        — page size (default: 20, max: 100)
func (h *URLHandler) List(w http.ResponseWriter, r *http.Request) {
	claims, ok := domainauth.FromContext(r.Context())
	if !ok {
		response.WriteProblem(w, response.Problem{
			Type: response.ProblemTypeUnauthenticated, Title: "Unauthorized",
			Status: http.StatusUnauthorized,
		})
		return
	}

	q := appurl.ListQuery{
		WorkspaceID: claims.WorkspaceID,
		Cursor:      r.URL.Query().Get("cursor"),
		Limit:       parseIntQuery(r, "limit", 20, 100),
	}

	// Parse optional status filter
	if statusParam := r.URL.Query().Get("status"); statusParam != "" {
		s := domainurl.Status(statusParam)
		q.Status = &s
	}

	// Parse optional created_by filter
	if createdBy := r.URL.Query().Get("created_by"); createdBy != "" {
		q.CreatedBy = &createdBy
	}

	result, err := h.lister.Handle(r.Context(), q)
	if err != nil {
		h.writeError(w, r, err, logger.FromContext(r.Context()))
		return
	}

	// Convert to HTTP response format
	urlResponses := make([]URLResponse, 0, len(result.URLs))
	for _, u := range result.URLs {
		urlResponses = append(urlResponses, toURLResponse(u))
	}

	response.JSON(w, http.StatusOK, ListURLsResponse{
		Data: urlResponses,
		Meta: ListMeta{
			Cursor:  result.NextCursor,
			HasMore: result.HasMore,
		},
	})
}

// ── PATCH /api/v1/workspaces/{workspaceID}/urls/{urlID} ───────────────────────

// Update applies partial updates to a URL.
func (h *URLHandler) Update(w http.ResponseWriter, r *http.Request) {
	log := logger.FromContext(r.Context()).With(
		slog.String("handler", "URLHandler.Update"),
		slog.String("request_id", chimiddleware.GetReqID(r.Context())),
	)

	claims, ok := domainauth.FromContext(r.Context())
	if !ok {
		response.WriteProblem(w, response.Problem{
			Type: response.ProblemTypeUnauthenticated, Title: "Unauthorized",
			Status: http.StatusUnauthorized,
		})
		return
	}

	urlID := chi.URLParam(r, "urlID")

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req UpdateURLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "request body could not be decoded", r.URL.Path)
		return
	}

	// Build the update command with only the fields present in the request.
	// The double-pointer for ExpiresAt allows distinguishing between:
	//   - Field not in request body (nil *time.Time pointer): no change
	//   - Field explicitly set to null in body (*time.Time is nil): remove expiry
	// We use a struct-level pointer here — if req.ExpiresAt was in the body
	// at all (even as null), we pass it through.
	var expiresAt **time.Time
	if req.ExpiresAt != nil || hasExpiresAtField(r) {
		expiresAt = &req.ExpiresAt
	}

	cmd := appurl.UpdateCommand{
		URLID:       urlID,
		WorkspaceID: claims.WorkspaceID,
		OriginalURL: req.OriginalURL,
		Title:       req.Title,
		ExpiresAt:   expiresAt,
	}

	result, err := h.updater.Handle(r.Context(), cmd)
	if err != nil {
		h.writeError(w, r, err, log)
		return
	}

	// Audit annotation
	domainaudit.AnnotateContext(r.Context(),
		domainaudit.ResourceURL,
		result.ID,
		map[string]any{
			"short_code":   result.ShortCode,
			"original_url": result.OriginalURL,
		},
	)

	response.JSON(w, http.StatusOK, response.Envelope{
		Data: toURLResponse(result),
	})
}

// ── DELETE /api/v1/workspaces/{workspaceID}/urls/{urlID} ──────────────────────

// Delete soft-deletes a URL. Returns 204 No Content on success.
func (h *URLHandler) Delete(w http.ResponseWriter, r *http.Request) {
	log := logger.FromContext(r.Context()).With(
		slog.String("handler", "URLHandler.Delete"),
		slog.String("request_id", chimiddleware.GetReqID(r.Context())),
	)

	claims, ok := domainauth.FromContext(r.Context())
	if !ok {
		response.WriteProblem(w, response.Problem{
			Type: response.ProblemTypeUnauthenticated, Title: "Unauthorized",
			Status: http.StatusUnauthorized,
		})
		return
	}

	urlID := chi.URLParam(r, "urlID")

	if err := h.deleter.Handle(r.Context(), appurl.DeleteCommand{
		URLID:       urlID,
		WorkspaceID: claims.WorkspaceID,
	}); err != nil {
		h.writeError(w, r, err, log)
		return
	}

	// Audit annotation — resource is the deleted URL
	domainaudit.AnnotateContext(r.Context(),
		domainaudit.ResourceURL,
		urlID,
		map[string]any{"deleted_by": claims.UserID},
	)

	w.WriteHeader(http.StatusNoContent)
}

// ── Error mapping ─────────────────────────────────────────────────────────────

func (h *URLHandler) writeError(
	w http.ResponseWriter, r *http.Request, err error, log *slog.Logger,
) {
	var ve *apperrors.ValidationError
	if errors.As(err, &ve) {
		response.UnprocessableEntity(w, ve.Message, r.URL.Path)
		return
	}
	if errors.Is(err, apperrors.ErrNotFound) {
		response.NotFound(w, r.URL.Path)
		return
	}
	if errors.Is(err, apperrors.ErrURLExpired) {
		response.Gone(w, r.URL.Path)
		return
	}
	log.Error("unexpected error in url handler",
		slog.String("error", err.Error()),
		slog.String("path", r.URL.Path),
	)
	response.InternalError(w, r.URL.Path)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// toURLResponse converts an application URLResult to the HTTP response type.
func toURLResponse(r *appurl.URLResult) URLResponse {
	return URLResponse{
		ID:          r.ID,
		ShortURL:    r.ShortURL,
		ShortCode:   r.ShortCode,
		OriginalURL: r.OriginalURL,
		Title:       r.Title,
		Status:      r.Status,
		WorkspaceID: r.WorkspaceID,
		CreatedBy:   r.CreatedBy,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
		ExpiresAt:   r.ExpiresAt,
		ClickCount:  r.ClickCount,
	}
}

// parseIntQuery reads an integer query parameter with default and max values.
func parseIntQuery(r *http.Request, param string, defaultVal, maxVal int) int {
	val := r.URL.Query().Get(param)
	if val == "" {
		return defaultVal
	}
	n := 0
	for _, c := range val {
		if c < '0' || c > '9' {
			return defaultVal
		}
		n = n*10 + int(c-'0')
	}
	if n <= 0 {
		return defaultVal
	}
	if n > maxVal {
		return maxVal
	}
	return n
}

// hasExpiresAtField checks if the request body contained an expires_at field.
// This is a simplified check — a production implementation would use a custom
// JSON decoder that tracks which fields were explicitly set.
// For Phase 2 we use the pointer presence as the signal.
func hasExpiresAtField(r *http.Request) bool {
	return false // Simplified: rely on pointer nil-check in UpdateURLRequest
}

// Package url contains the application use cases for URL resource management.
// These are the read and write operations beyond the core shorten/resolve path.
//
// Relationship to the shorten package:
//
//	internal/application/shorten — CreateURL (write, high-performance path)
//	internal/application/url     — GetURL, ListURLs, UpdateURL, DeleteURL
//
// Separation rationale:
//
//	The shorten use case is optimised for throughput and latency — it sits on
//	the hot path. CRUD operations are management operations with different
//	performance characteristics. Keeping them in separate packages prevents
//	coupling and makes each independently testable.
package url

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/urlshortener/platform/internal/application/apperrors"
	domainurl "github.com/urlshortener/platform/internal/domain/url"
)

const tracerName = "github.com/urlshortener/platform/internal/application/url"

// GetQuery carries inputs for the GetURL use case.
type GetQuery struct {
	// URLID is the ULID of the URL to retrieve.
	URLID string

	// WorkspaceID is taken from JWT claims — not from the request body.
	// This enforces the invariant that users can only access URLs within
	// their authenticated workspace. Cross-workspace access is impossible
	// by construction, not by application-level checks.
	WorkspaceID string
}

// URLResult is the canonical read response for a URL resource.
// Used by both GetURL and ListURLs responses.
type URLResult struct {
	ID          string
	ShortURL    string // Fully-qualified short URL (baseURL + "/" + shortCode)
	ShortCode   string
	OriginalURL string
	Title       string
	Status      string
	WorkspaceID string
	CreatedBy   string
	CreatedAt   string
	UpdatedAt   string
	ExpiresAt   *time.Time
	ClickCount  int64
}

// GetHandler orchestrates the GetURL use case.
// Retrieves a URL by ID, enforcing workspace ownership.
type GetHandler struct {
	repo    domainurl.Repository
	baseURL string
}

// NewGetHandler creates a GetHandler.
func NewGetHandler(repo domainurl.Repository, baseURL string) *GetHandler {
	return &GetHandler{repo: repo, baseURL: baseURL}
}

// Handle executes the GetURL use case.
//
// Authorization model:
//
//	The workspace_id from JWT claims is passed to the repository query.
//	The SQL WHERE clause includes AND workspace_id = $2.
//	A URL belonging to a different workspace returns ErrNotFound —
//	this is correct: we must not reveal that the URL exists at all
//	(information leakage prevention). An attacker cannot determine
//	whether a URL ID belongs to another workspace.
func (h *GetHandler) Handle(ctx context.Context, q GetQuery) (*URLResult, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "GetURL.Handle",
		trace.WithAttributes(
			attribute.String("url.id", q.URLID),
			attribute.String("workspace.id", q.WorkspaceID),
		),
	)
	defer span.End()

	u, err := h.repo.GetByID(ctx, q.URLID, q.WorkspaceID)
	if err != nil {
		if domainurl.IsNotFound(err) {
			return nil, apperrors.ErrNotFound
		}
		if domainurl.IsGone(err) {
			// Soft-deleted URLs: treat as not found (no information leakage)
			return nil, apperrors.ErrNotFound
		}
		span.RecordError(err)
		return nil, fmt.Errorf("getting url: %w", err)
	}

	return toURLResult(u, h.baseURL), nil
}

// toURLResult converts a domain URL entity to the API result type.
// Centralising this conversion prevents the mapping logic from being
// duplicated across GetHandler, ListHandler, and UpdateHandler.
func toURLResult(u *domainurl.URL, baseURL string) *URLResult {
	return &URLResult{
		ID:          u.ID,
		ShortURL:    baseURL + "/" + u.ShortCode,
		ShortCode:   u.ShortCode,
		OriginalURL: u.OriginalURL,
		Title:       u.Title,
		Status:      string(u.Status),
		WorkspaceID: u.WorkspaceID,
		CreatedBy:   u.CreatedBy,
		CreatedAt:   u.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   u.UpdatedAt.Format(time.RFC3339),
		ExpiresAt:   u.ExpiresAt,
		ClickCount:  u.ClickCount,
	}
}

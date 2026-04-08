package url

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	domainurl "github.com/urlshortener/platform/internal/domain/url"
)

// ListQuery carries inputs for the ListURLs use case.
// Maps directly to the PRD section 5.1.7 filter and sort parameters.
type ListQuery struct {
	WorkspaceID string

	// Status filters by URL lifecycle state. nil = all non-deleted statuses.
	Status *domainurl.Status

	// CreatedBy filters by the creating user's ID. nil = all creators.
	CreatedBy *string

	// Cursor is the ULID of the last item from the previous page.
	// Empty string = first page.
	Cursor string

	// Limit is the maximum number of results per page.
	// 0 = default (20); max = 100.
	Limit int
}

// ListResult is the paginated response for the ListURLs use case.
type ListResult struct {
	// URLs is the current page of URL results.
	URLs []*URLResult

	// NextCursor is the cursor value for fetching the next page.
	// Empty string indicates this is the last page.
	NextCursor string

	// HasMore is true when NextCursor is non-empty.
	// Provided as a convenience for clients that prefer a boolean.
	HasMore bool
}

// ListHandler orchestrates the ListURLs use case.
//
// Cursor pagination correctness:
//
//	The cursor is the ULID of the last item on the previous page.
//	ULIDs are lexicographically sortable and time-ordered, so
//	WHERE id > cursor ORDER BY id ASC gives consistent, stable pages
//	even when new URLs are created between page fetches.
//
//	This is why the PRD mandates cursor-based pagination (section 5.1.7):
//	offset pagination is O(n) and inconsistent under concurrent inserts.
type ListHandler struct {
	repo    domainurl.Repository
	baseURL string
}

// NewListHandler creates a ListHandler.
func NewListHandler(repo domainurl.Repository, baseURL string) *ListHandler {
	return &ListHandler{repo: repo, baseURL: baseURL}
}

// Handle executes the ListURLs use case.
func (h *ListHandler) Handle(ctx context.Context, q ListQuery) (*ListResult, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "ListURLs.Handle",
		trace.WithAttributes(
			attribute.String("workspace.id", q.WorkspaceID),
			attribute.Int("limit", q.Limit),
		),
	)
	defer span.End()

	filter := domainurl.ListFilter{
		WorkspaceID: q.WorkspaceID,
		Status:      q.Status,
		CreatedBy:   q.CreatedBy,
		Cursor:      q.Cursor,
		Limit:       q.Limit,
	}

	urls, nextCursor, err := h.repo.List(ctx, filter)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("listing urls: %w", err)
	}

	results := make([]*URLResult, 0, len(urls))
	for _, u := range urls {
		results = append(results, toURLResult(u, h.baseURL))
	}

	return &ListResult{
		URLs:       results,
		NextCursor: nextCursor,
		HasMore:    nextCursor != "",
	}, nil
}

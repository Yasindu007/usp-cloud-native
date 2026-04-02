package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	domainurl "github.com/urlshortener/platform/internal/domain/url"
)

// tracerName is the OpenTelemetry tracer name for this package.
// Convention: use the fully-qualified package import path.
const tracerName = "github.com/urlshortener/platform/internal/infrastructure/postgres"

// URLRepository implements domain/url.Repository and domain/url.ReadonlyRepository
// using PostgreSQL via the pgx v5 driver.
//
// Every method:
//  1. Creates an OTel span for distributed tracing
//  2. Executes a fully parameterized query (no string interpolation)
//  3. Translates pgx errors to domain errors at the return boundary
//  4. Propagates context cancellation (query is cancelled if request times out)
type URLRepository struct {
	db *Client
}

// NewURLRepository creates a new URLRepository backed by the given Client.
// The Client provides both primary (write) and replica (read) pool access.
func NewURLRepository(db *Client) *URLRepository {
	return &URLRepository{db: db}
}

// ── Write operations (use Primary pool) ──────────────────────────────────────

// Create inserts a new URL record into the database.
// The caller is responsible for generating the ULID id and short code
// before calling this method (done by the application layer).
//
// On short_code collision, returns domain/url.ErrConflict so the
// application layer can retry with a new code (max 3 attempts).
func (r *URLRepository) Create(ctx context.Context, u *domainurl.URL) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "URLRepository.Create",
		trace.WithAttributes(
			attribute.String("db.operation", "INSERT"),
			attribute.String("db.table", "urls"),
			attribute.String("url.short_code", u.ShortCode),
		),
	)
	defer span.End()

	// All 12 columns are explicitly listed — never use SELECT * or INSERT without
	// column names. Explicit columns make the query immune to column reordering
	// and adding nullable columns to the table.
	const query = `
		INSERT INTO urls (
			id, workspace_id, short_code, original_url,
			title, status, expires_at, created_by,
			created_at, updated_at, deleted_at, click_count
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8,
			$9, $10, $11, $12
		)`

	_, err := r.db.Primary().Exec(ctx, query,
		u.ID,
		u.WorkspaceID,
		u.ShortCode,
		u.OriginalURL,
		u.Title,
		string(u.Status),
		u.ExpiresAt, // pgx handles *time.Time as NULL correctly
		u.CreatedBy,
		u.CreatedAt,
		u.UpdatedAt,
		u.DeletedAt,
		u.ClickCount,
	)
	if err != nil {
		span.RecordError(err)
		return translateError(err, "short_code")
	}

	return nil
}

// Update persists mutations to an existing URL.
// Only the mutable fields are updated — id, created_at, and created_by
// are immutable once set. updated_at is handled by the DB trigger.
//
// Note: we update all mutable fields unconditionally (full update), not just
// changed fields. For a PATCH endpoint, the application layer reads the
// current record first, applies changes, then calls Update with the full entity.
// This is simpler and safer than a dynamic partial-update query.
func (r *URLRepository) Update(ctx context.Context, u *domainurl.URL) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "URLRepository.Update",
		trace.WithAttributes(
			attribute.String("db.operation", "UPDATE"),
			attribute.String("db.table", "urls"),
			attribute.String("url.id", u.ID),
		),
	)
	defer span.End()

	const query = `
		UPDATE urls SET
			original_url = $1,
			title        = $2,
			status       = $3,
			expires_at   = $4
		WHERE id = $5
		  AND workspace_id = $6
		  AND deleted_at IS NULL`

	tag, err := r.db.Primary().Exec(ctx, query,
		u.OriginalURL,
		u.Title,
		string(u.Status),
		u.ExpiresAt,
		u.ID,
		u.WorkspaceID,
	)
	if err != nil {
		span.RecordError(err)
		return translateError(err, "urls")
	}

	// RowsAffected = 0 means the WHERE clause matched no rows.
	// This could mean: wrong ID, wrong workspace_id, or already deleted.
	// We return ErrNotFound in all cases (no information leakage).
	if tag.RowsAffected() == 0 {
		return domainurl.ErrNotFound
	}

	return nil
}

// SoftDelete marks a URL as deleted without removing the database row.
// The URL is immediately unresolvable (WHERE deleted_at IS NULL filters it out).
// Hard deletion happens via a scheduled purge job after 90-day retention.
func (r *URLRepository) SoftDelete(ctx context.Context, id string, workspaceID string) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "URLRepository.SoftDelete",
		trace.WithAttributes(
			attribute.String("db.operation", "UPDATE"),
			attribute.String("db.table", "urls"),
			attribute.String("url.id", id),
		),
	)
	defer span.End()

	const query = `
		UPDATE urls SET
			status     = 'deleted',
			deleted_at = $1
		WHERE id = $2
		  AND workspace_id = $3
		  AND deleted_at IS NULL`

	tag, err := r.db.Primary().Exec(ctx, query,
		time.Now().UTC(),
		id,
		workspaceID,
	)
	if err != nil {
		span.RecordError(err)
		return translateError(err, "urls")
	}

	if tag.RowsAffected() == 0 {
		return domainurl.ErrNotFound
	}

	return nil
}

// IncrementClickCount atomically increments the click counter for a short code.
// Uses UPDATE ... SET click_count = click_count + 1 which is atomic at the
// PostgreSQL row level — no separate read-modify-write cycle needed.
//
// This is called asynchronously from the redirect hot path (fire-and-forget).
// It uses the primary pool because it is a write operation, but is designed
// to never block the redirect response.
func (r *URLRepository) IncrementClickCount(ctx context.Context, shortCode string) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "URLRepository.IncrementClickCount",
		trace.WithAttributes(
			attribute.String("db.operation", "UPDATE"),
			attribute.String("url.short_code", shortCode),
		),
	)
	defer span.End()

	const query = `
		UPDATE urls
		SET click_count = click_count + 1
		WHERE short_code = $1
		  AND deleted_at IS NULL`

	_, err := r.db.Primary().Exec(ctx, query, shortCode)
	if err != nil {
		span.RecordError(err)
		return translateError(err, "click_count")
	}

	return nil
}

// ── Read operations (use Replica pool) ──────────────────────────────────────

// GetByShortCode retrieves a URL by its short code.
// This is the hottest query in the system — called on every redirect.
// It uses the replica pool because it is a read operation.
//
// The query includes expires_at and status so the application layer
// can call u.CanRedirect() without a second database round-trip.
//
// Performance note: this query hits the urls_short_code_unique index
// (B-tree on short_code) and returns in O(log n) time regardless of
// total table size. At 50M URLs the index is ~800MB — well within RAM.
func (r *URLRepository) GetByShortCode(ctx context.Context, shortCode string) (*domainurl.URL, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "URLRepository.GetByShortCode",
		trace.WithAttributes(
			attribute.String("db.operation", "SELECT"),
			attribute.String("db.table", "urls"),
			attribute.String("url.short_code", shortCode),
		),
	)
	defer span.End()

	const query = `
		SELECT
			id, workspace_id, short_code, original_url,
			title, status, expires_at, created_by,
			created_at, updated_at, deleted_at, click_count
		FROM urls
		WHERE short_code = $1`

	row := r.db.Replica().QueryRow(ctx, query, shortCode)

	u, err := scanURL(row)
	if err != nil {
		span.RecordError(err)
		return nil, translateError(err, "short_code")
	}

	// Check soft-delete at the application level.
	// We deliberately do NOT filter deleted_at IS NULL in the SQL above
	// so we can return ErrDeleted (distinct from ErrNotFound) for audit purposes.
	if u.DeletedAt != nil {
		return nil, domainurl.ErrDeleted
	}

	return u, nil
}

// GetByID retrieves a URL by its ULID, scoped to a workspace.
// The workspace_id filter ensures tenants cannot access each other's URLs.
func (r *URLRepository) GetByID(ctx context.Context, id string, workspaceID string) (*domainurl.URL, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "URLRepository.GetByID",
		trace.WithAttributes(
			attribute.String("db.operation", "SELECT"),
			attribute.String("db.table", "urls"),
			attribute.String("url.id", id),
		),
	)
	defer span.End()

	const query = `
		SELECT
			id, workspace_id, short_code, original_url,
			title, status, expires_at, created_by,
			created_at, updated_at, deleted_at, click_count
		FROM urls
		WHERE id = $1
		  AND workspace_id = $2`

	row := r.db.Replica().QueryRow(ctx, query, id, workspaceID)

	u, err := scanURL(row)
	if err != nil {
		span.RecordError(err)
		return nil, translateError(err, "id")
	}

	if u.DeletedAt != nil {
		return nil, domainurl.ErrDeleted
	}

	return u, nil
}

// List returns a paginated list of URLs for a workspace using cursor-based pagination.
//
// Cursor-based pagination design:
//   - The cursor is the ULID of the last item on the previous page.
//   - ULIDs are lexicographically sortable (time-ordered), so WHERE id > cursor
//     gives us the correct next page without an OFFSET scan.
//   - OFFSET pagination (LIMIT 20 OFFSET 10000) requires scanning 10,000 rows
//     to discard before returning 20 — O(n) gets worse as pages go deeper.
//   - Cursor pagination is O(log n) regardless of page depth.
//
// The caller receives a nextCursor string. If empty, there are no more pages.
func (r *URLRepository) List(ctx context.Context, filter domainurl.ListFilter) ([]*domainurl.URL, string, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "URLRepository.List",
		trace.WithAttributes(
			attribute.String("db.operation", "SELECT"),
			attribute.String("db.table", "urls"),
			attribute.String("filter.workspace_id", filter.WorkspaceID),
		),
	)
	defer span.End()

	limit := filter.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	// We fetch limit+1 rows. If we get limit+1 results, there is a next page
	// and we return the last item's ID as the cursor. We only return limit items.
	// This avoids a separate COUNT query.
	fetchLimit := limit + 1

	// Build parameterized query. Using a fixed query structure (not dynamic SQL)
	// is intentional — all variations use the same query plan, which PostgreSQL
	// can cache. Dynamic SQL defeats query plan caching.
	const query = `
		SELECT
			id, workspace_id, short_code, original_url,
			title, status, expires_at, created_by,
			created_at, updated_at, deleted_at, click_count
		FROM urls
		WHERE workspace_id = $1
		  AND deleted_at IS NULL
		  AND ($2::text = '' OR id > $2)
		  AND ($3::text = '' OR status = $3)
		  AND ($4::text = '' OR created_by = $4)
		ORDER BY id ASC
		LIMIT $5`

	statusFilter := ""
	if filter.Status != nil {
		statusFilter = string(*filter.Status)
	}

	createdByFilter := ""
	if filter.CreatedBy != nil {
		createdByFilter = *filter.CreatedBy
	}

	rows, err := r.db.Replica().Query(ctx, query,
		filter.WorkspaceID,
		filter.Cursor,
		statusFilter,
		createdByFilter,
		fetchLimit,
	)
	if err != nil {
		span.RecordError(err)
		return nil, "", translateError(err, "list")
	}
	defer rows.Close()

	var urls []*domainurl.URL
	for rows.Next() {
		u, err := scanURLFromRows(rows)
		if err != nil {
			span.RecordError(err)
			return nil, "", fmt.Errorf("scanning url row: %w", err)
		}
		urls = append(urls, u)
	}

	if err := rows.Err(); err != nil {
		span.RecordError(err)
		return nil, "", translateError(err, "list rows iteration")
	}

	// Determine next cursor.
	nextCursor := ""
	if len(urls) > limit {
		// We have more pages. The cursor is the ID of the last item we'll return.
		nextCursor = urls[limit-1].ID
		urls = urls[:limit] // Trim the extra item
	}

	return urls, nextCursor, nil
}

// ── Scanning helpers ──────────────────────────────────────────────────────────

// pgxRowScanner is a minimal interface satisfied by both *pgx.Row and pgx.Rows.
// This allows scanURL to work with both QueryRow (single row) and Query (multi-row).
type pgxRowScanner interface {
	Scan(dest ...any) error
}

// scanURL scans a single database row into a URL domain entity.
// Column order MUST match the SELECT column list in every query above.
// Any mismatch causes a runtime panic — always verify column order
// when modifying queries.
func scanURL(row pgxRowScanner) (*domainurl.URL, error) {
	var u domainurl.URL
	var status string

	err := row.Scan(
		&u.ID,
		&u.WorkspaceID,
		&u.ShortCode,
		&u.OriginalURL,
		&u.Title,
		&status,
		&u.ExpiresAt,
		&u.CreatedBy,
		&u.CreatedAt,
		&u.UpdatedAt,
		&u.DeletedAt,
		&u.ClickCount,
	)
	if err != nil {
		return nil, err
	}

	u.Status = domainurl.Status(status)
	return &u, nil
}

// scanURLFromRows scans from a pgx.Rows cursor (used in List).
func scanURLFromRows(rows pgx.Rows) (*domainurl.URL, error) {
	return scanURL(rows)
}

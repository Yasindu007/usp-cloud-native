package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	domainexport "github.com/urlshortener/platform/internal/domain/export"
)

const exportTracerName = "github.com/urlshortener/platform/internal/infrastructure/postgres/export"

type ExportRepository struct {
	db *Client
}

func NewExportRepository(db *Client) *ExportRepository {
	return &ExportRepository{db: db}
}

func (r *ExportRepository) Create(ctx context.Context, e *domainexport.Export) error {
	ctx, span := otel.Tracer(exportTracerName).Start(ctx, "ExportRepository.Create")
	defer span.End()

	const query = `
		INSERT INTO exports (
			id, workspace_id, requested_by, format, status,
			date_from, date_to, include_bots, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

	_, err := r.db.Primary().Exec(ctx, query,
		e.ID, e.WorkspaceID, e.RequestedBy, string(e.Format), string(e.Status),
		e.DateFrom, e.DateTo, e.IncludeBots, e.CreatedAt,
	)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("export: inserting job: %w", err)
	}
	return nil
}

func (r *ExportRepository) GetByID(ctx context.Context, id, workspaceID string) (*domainexport.Export, error) {
	ctx, span := otel.Tracer(exportTracerName).Start(ctx, "ExportRepository.GetByID",
		trace.WithAttributes(attribute.String("export.id", id)),
	)
	defer span.End()

	const query = `
		SELECT id, workspace_id, requested_by, format, status,
		       date_from, date_to, include_bots,
		       COALESCE(file_path, ''), COALESCE(row_count, 0),
		       COALESCE(file_size_bytes, 0), COALESCE(error_message, ''),
		       COALESCE(download_token, ''), download_expires_at,
		       created_at, worker_started_at, completed_at
		FROM exports
		WHERE id = $1 AND workspace_id = $2`

	row := r.db.Replica().QueryRow(ctx, query, id, workspaceID)
	e, err := scanExport(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, domainexport.ErrNotFound
		}
		span.RecordError(err)
		return nil, fmt.Errorf("export: get by id: %w", err)
	}
	return e, nil
}

func (r *ExportRepository) GetAnyByID(ctx context.Context, id string) (*domainexport.Export, error) {
	const query = `
		SELECT id, workspace_id, requested_by, format, status,
		       date_from, date_to, include_bots,
		       COALESCE(file_path, ''), COALESCE(row_count, 0),
		       COALESCE(file_size_bytes, 0), COALESCE(error_message, ''),
		       COALESCE(download_token, ''), download_expires_at,
		       created_at, worker_started_at, completed_at
		FROM exports
		WHERE id = $1`
	row := r.db.Replica().QueryRow(ctx, query, id)
	e, err := scanExport(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, domainexport.ErrNotFound
		}
		return nil, fmt.Errorf("export: get any by id: %w", err)
	}
	return e, nil
}

func (r *ExportRepository) ListByWorkspace(ctx context.Context, workspaceID string, limit int) ([]*domainexport.Export, error) {
	if limit <= 0 {
		limit = 20
	}
	const query = `
		SELECT id, workspace_id, requested_by, format, status,
		       date_from, date_to, include_bots,
		       COALESCE(file_path, ''), COALESCE(row_count, 0),
		       COALESCE(file_size_bytes, 0), COALESCE(error_message, ''),
		       COALESCE(download_token, ''), download_expires_at,
		       created_at, worker_started_at, completed_at
		FROM exports
		WHERE workspace_id = $1
		ORDER BY created_at DESC
		LIMIT $2`
	rows, err := r.db.Replica().Query(ctx, query, workspaceID, limit)
	if err != nil {
		return nil, fmt.Errorf("export: list by workspace: %w", err)
	}
	defer rows.Close()

	var out []*domainexport.Export
	for rows.Next() {
		e, err := scanExport(rows)
		if err != nil {
			return nil, fmt.Errorf("export: scan list row: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("export: list rows: %w", err)
	}
	return out, nil
}

func (r *ExportRepository) ClaimPending(ctx context.Context) (*domainexport.Export, error) {
	ctx, span := otel.Tracer(exportTracerName).Start(ctx, "ExportRepository.ClaimPending")
	defer span.End()

	const query = `
		WITH next_job AS (
			SELECT id
			FROM exports
			WHERE status = 'pending'
			ORDER BY created_at ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE exports e
		SET status = 'processing',
		    worker_started_at = NOW()
		FROM next_job
		WHERE e.id = next_job.id
		RETURNING e.id, e.workspace_id, e.requested_by, e.format, e.status,
		          e.date_from, e.date_to, e.include_bots,
		          COALESCE(e.file_path, ''), COALESCE(e.row_count, 0),
		          COALESCE(e.file_size_bytes, 0), COALESCE(e.error_message, ''),
		          COALESCE(e.download_token, ''), e.download_expires_at,
		          e.created_at, e.worker_started_at, e.completed_at`
	row := r.db.Primary().QueryRow(ctx, query)
	e, err := scanExport(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		span.RecordError(err)
		return nil, fmt.Errorf("export: claim pending: %w", err)
	}
	return e, nil
}

func (r *ExportRepository) MarkCompleted(ctx context.Context, id, filePath string, rowCount, fileSizeBytes int64, token string, expiresAt time.Time) error {
	const query = `
		UPDATE exports
		SET status = 'completed',
		    file_path = $2,
		    row_count = $3,
		    file_size_bytes = $4,
		    download_token = $5,
		    download_expires_at = $6,
		    completed_at = NOW(),
		    error_message = ''
		WHERE id = $1`
	_, err := r.db.Primary().Exec(ctx, query, id, filePath, rowCount, fileSizeBytes, token, expiresAt)
	if err != nil {
		return fmt.Errorf("export: mark completed: %w", err)
	}
	return nil
}

func (r *ExportRepository) MarkFailed(ctx context.Context, id, errorMessage string) error {
	const query = `
		UPDATE exports
		SET status = 'failed',
		    error_message = $2,
		    completed_at = NOW()
		WHERE id = $1`
	_, err := r.db.Primary().Exec(ctx, query, id, errorMessage)
	if err != nil {
		return fmt.Errorf("export: mark failed: %w", err)
	}
	return nil
}

func (r *ExportRepository) ReadEvents(ctx context.Context, q domainexport.EventQuery) (<-chan *domainexport.RedirectEventRow, <-chan error) {
	rowsCh := make(chan *domainexport.RedirectEventRow)
	errCh := make(chan error, 1)

	go func() {
		defer close(rowsCh)
		defer close(errCh)

		query := `
			SELECT short_code, occurred_at,
			       COALESCE(country_code::text, ''),
			       COALESCE(device_type, ''),
			       COALESCE(browser_family, ''),
			       COALESCE(os_family, ''),
			       COALESCE(referrer_domain, ''),
			       is_bot
			FROM redirect_events
			WHERE workspace_id = $1
			  AND occurred_at >= $2
			  AND occurred_at <= $3`
		args := []any{q.WorkspaceID, q.DateFrom, q.DateTo}
		if !q.IncludeBots {
			query += ` AND is_bot = false`
		}
		query += ` ORDER BY occurred_at ASC`

		rows, err := r.db.Replica().Query(ctx, query, args...)
		if err != nil {
			errCh <- fmt.Errorf("export: query redirect events: %w", err)
			return
		}
		defer rows.Close()

		for rows.Next() {
			var row domainexport.RedirectEventRow
			if err := rows.Scan(
				&row.ShortCode,
				&row.OccurredAt,
				&row.CountryCode,
				&row.DeviceType,
				&row.BrowserFamily,
				&row.OSFamily,
				&row.ReferrerDomain,
				&row.IsBot,
			); err != nil {
				errCh <- fmt.Errorf("export: scan redirect event: %w", err)
				return
			}
			select {
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			case rowsCh <- &row:
			}
		}
		if err := rows.Err(); err != nil {
			errCh <- fmt.Errorf("export: iterate redirect events: %w", err)
		}
	}()

	return rowsCh, errCh
}

type exportScanner interface {
	Scan(dest ...any) error
}

func scanExport(row exportScanner) (*domainexport.Export, error) {
	var e domainexport.Export
	var format string
	var status string
	if err := row.Scan(
		&e.ID,
		&e.WorkspaceID,
		&e.RequestedBy,
		&format,
		&status,
		&e.DateFrom,
		&e.DateTo,
		&e.IncludeBots,
		&e.FilePath,
		&e.RowCount,
		&e.FileSizeBytes,
		&e.ErrorMessage,
		&e.DownloadToken,
		&e.DownloadExpiresAt,
		&e.CreatedAt,
		&e.WorkerStartedAt,
		&e.CompletedAt,
	); err != nil {
		return nil, err
	}
	e.Format = domainexport.Format(format)
	e.Status = domainexport.Status(status)
	return &e, nil
}

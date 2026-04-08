package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	domainapikey "github.com/urlshortener/platform/internal/domain/apikey"
)

const apikeyTracerName = "github.com/urlshortener/platform/internal/infrastructure/postgres/apikey"

// APIKeyRepository implements domain/apikey.Repository using pgx v5.
type APIKeyRepository struct {
	db *Client
}

// NewAPIKeyRepository creates an APIKeyRepository.
func NewAPIKeyRepository(db *Client) *APIKeyRepository {
	return &APIKeyRepository{db: db}
}

// Create persists a new API key.
// The RawKey field on the entity is NOT written to the database —
// only KeyHash, KeyPrefix, and metadata are stored.
func (r *APIKeyRepository) Create(ctx context.Context, k *domainapikey.APIKey) error {
	ctx, span := otel.Tracer(apikeyTracerName).Start(ctx, "APIKeyRepository.Create",
		trace.WithAttributes(
			attribute.String("workspace.id", k.WorkspaceID),
		),
	)
	defer span.End()

	const query = `
		INSERT INTO api_keys
			(id, workspace_id, name, key_hash, key_prefix, scopes,
			 created_by, created_at, expires_at, revoked_at, last_used_at)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8, $9, NULL, NULL)`

	_, err := r.db.Primary().Exec(ctx, query,
		k.ID,
		k.WorkspaceID,
		k.Name,
		k.KeyHash, // bcrypt hash — never the raw key
		k.KeyPrefix,
		k.Scopes, // pgx converts []string to TEXT[] automatically
		k.CreatedBy,
		k.CreatedAt,
		k.ExpiresAt,
	)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("inserting api key: %w", translateError(err, "api_keys"))
	}
	return nil
}

// GetByPrefix returns all active keys matching the given prefix.
// "Active" means: revoked_at IS NULL AND (expires_at IS NULL OR expires_at > NOW()).
//
// This query runs on the primary pool because it must be consistent with
// just-created keys — the replica may lag and miss a newly created key,
// causing authentication to fail for the creator.
func (r *APIKeyRepository) GetByPrefix(ctx context.Context, prefix string) ([]*domainapikey.APIKey, error) {
	ctx, span := otel.Tracer(apikeyTracerName).Start(ctx, "APIKeyRepository.GetByPrefix",
		trace.WithAttributes(
			attribute.String("apikey.prefix", prefix),
		),
	)
	defer span.End()

	const query = `
		SELECT id, workspace_id, name, key_hash, key_prefix, scopes,
		       created_by, created_at, expires_at, revoked_at, last_used_at
		FROM api_keys
		WHERE key_prefix = $1
		  AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > NOW())`

	rows, err := r.db.Primary().Query(ctx, query, prefix)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("querying api keys by prefix: %w", err)
	}
	defer rows.Close()

	var keys []*domainapikey.APIKey
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning api key: %w", err)
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// GetByID retrieves an API key by its ULID, scoped to a workspace.
func (r *APIKeyRepository) GetByID(ctx context.Context, id, workspaceID string) (*domainapikey.APIKey, error) {
	ctx, span := otel.Tracer(apikeyTracerName).Start(ctx, "APIKeyRepository.GetByID",
		trace.WithAttributes(
			attribute.String("apikey.id", id),
		),
	)
	defer span.End()

	const query = `
		SELECT id, workspace_id, name, key_hash, key_prefix, scopes,
		       created_by, created_at, expires_at, revoked_at, last_used_at
		FROM api_keys
		WHERE id = $1 AND workspace_id = $2`

	row := r.db.Replica().QueryRow(ctx, query, id, workspaceID)
	k, err := scanAPIKey(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, domainapikey.ErrNotFound
		}
		span.RecordError(err)
		return nil, fmt.Errorf("getting api key by id: %w", err)
	}
	return k, nil
}

// List returns all active API keys for a workspace, ordered by created_at DESC.
func (r *APIKeyRepository) List(ctx context.Context, workspaceID string) ([]*domainapikey.APIKey, error) {
	ctx, span := otel.Tracer(apikeyTracerName).Start(ctx, "APIKeyRepository.List",
		trace.WithAttributes(
			attribute.String("workspace.id", workspaceID),
		),
	)
	defer span.End()

	const query = `
		SELECT id, workspace_id, name, key_hash, key_prefix, scopes,
		       created_by, created_at, expires_at, revoked_at, last_used_at
		FROM api_keys
		WHERE workspace_id = $1
		  AND revoked_at IS NULL
		ORDER BY created_at DESC`

	rows, err := r.db.Replica().Query(ctx, query, workspaceID)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("listing api keys: %w", err)
	}
	defer rows.Close()

	var keys []*domainapikey.APIKey
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning api key: %w", err)
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// Revoke soft-deletes an API key by setting revoked_at = now().
func (r *APIKeyRepository) Revoke(ctx context.Context, id, workspaceID string) error {
	ctx, span := otel.Tracer(apikeyTracerName).Start(ctx, "APIKeyRepository.Revoke",
		trace.WithAttributes(
			attribute.String("apikey.id", id),
		),
	)
	defer span.End()

	const query = `
		UPDATE api_keys
		SET revoked_at = NOW()
		WHERE id = $1
		  AND workspace_id = $2
		  AND revoked_at IS NULL`

	tag, err := r.db.Primary().Exec(ctx, query, id, workspaceID)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("revoking api key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Either not found, wrong workspace, or already revoked.
		// Check which by querying with revoked_at included.
		var revokedAt *time.Time
		checkErr := r.db.Primary().QueryRow(ctx,
			`SELECT revoked_at FROM api_keys WHERE id = $1 AND workspace_id = $2`,
			id, workspaceID,
		).Scan(&revokedAt)

		if checkErr == pgx.ErrNoRows {
			return domainapikey.ErrNotFound
		}
		if revokedAt != nil {
			return domainapikey.ErrAlreadyRevoked
		}
		return domainapikey.ErrNotFound
	}
	return nil
}

// UpdateLastUsed sets last_used_at = now().
// Runs on the primary pool. Called asynchronously — never blocks requests.
func (r *APIKeyRepository) UpdateLastUsed(ctx context.Context, id string) error {
	_, err := r.db.Primary().Exec(ctx,
		`UPDATE api_keys SET last_used_at = NOW() WHERE id = $1`, id,
	)
	return err
}

// ── Scanning ──────────────────────────────────────────────────────────────────

type apikeyRowScanner interface {
	Scan(dest ...any) error
}

func scanAPIKey(row apikeyRowScanner) (*domainapikey.APIKey, error) {
	var k domainapikey.APIKey
	err := row.Scan(
		&k.ID,
		&k.WorkspaceID,
		&k.Name,
		&k.KeyHash,
		&k.KeyPrefix,
		&k.Scopes, // pgx scans TEXT[] into []string
		&k.CreatedBy,
		&k.CreatedAt,
		&k.ExpiresAt,
		&k.RevokedAt,
		&k.LastUsedAt,
	)
	if err != nil {
		return nil, err
	}
	// RawKey is never populated from DB — it is generation-time only.
	return &k, nil
}

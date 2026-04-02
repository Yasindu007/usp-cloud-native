package postgres

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	domainurl "github.com/urlshortener/platform/internal/domain/url"
)

// PostgreSQL error codes we handle explicitly.
// Full list: https://www.postgresql.org/docs/current/errcodes-appendix.html
const (
	// pgErrUniqueViolation is raised when a UNIQUE constraint is violated.
	// We see this when inserting a short_code that already exists.
	pgErrUniqueViolation = "23505"

	// pgErrForeignKeyViolation is raised when a FK constraint is violated.
	pgErrForeignKeyViolation = "23503"

	// pgErrNotNullViolation is raised when a NOT NULL constraint is violated.
	pgErrNotNullViolation = "23502"

	// pgErrCheckViolation is raised when a CHECK constraint is violated.
	// Example: inserting a status value not in ('active','expired','disabled','deleted').
	pgErrCheckViolation = "23514"

	// pgErrConnectionException is a class prefix for connection failures.
	pgErrConnectionException = "08"
)

// translateError converts pgx/PostgreSQL driver errors into domain errors.
// This is the adapter boundary: infrastructure concerns (DB error codes)
// are translated into domain concerns (domain errors) here, so they never
// leak into the application or domain layers.
//
// Usage pattern:
//
//	if err != nil {
//	    return translateError(err, "short_code")
//	}
//
// The constraintName parameter is the column/constraint involved in the
// operation, used for generating descriptive error messages.
func translateError(err error, context string) error {
	if err == nil {
		return nil
	}

	// pgx.ErrNoRows is the equivalent of sql.ErrNoRows.
	// It is returned by QueryRow().Scan() when no row matches the query.
	if errors.Is(err, pgx.ErrNoRows) {
		return domainurl.ErrNotFound
	}

	// Check for PostgreSQL-specific error codes.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case pgErrUniqueViolation:
			// A unique constraint violation on short_code means the generated
			// code collides with an existing one. The application layer handles
			// this by retrying with a new code (up to 3 attempts).
			return fmt.Errorf("%w: %s constraint violation on %s",
				domainurl.ErrConflict, pgErr.ConstraintName, context)

		case pgErrForeignKeyViolation:
			return fmt.Errorf("foreign key constraint violation: %s", pgErr.ConstraintName)

		case pgErrCheckViolation:
			return fmt.Errorf("check constraint violation: %s on %s", pgErr.ConstraintName, context)

		case pgErrNotNullViolation:
			return fmt.Errorf("not null constraint violation on column: %s", pgErr.ColumnName)
		}

		// Connection class errors — wrap so the application can detect them.
		if len(pgErr.Code) >= 2 && pgErr.Code[:2] == pgErrConnectionException {
			return fmt.Errorf("database connection error: %w", err)
		}
	}

	// All other errors are wrapped and returned as-is.
	// The application layer should treat unrecognized DB errors as internal errors.
	return fmt.Errorf("database error: %w", err)
}

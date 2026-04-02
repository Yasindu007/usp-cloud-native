// Package apperrors defines application-level errors that cross the boundary
// between the application layer and the interfaces (HTTP handler) layer.
//
// Error layer separation:
//
//	domain/url/errors.go  — domain invariant errors (ErrNotFound, ErrConflict)
//	application/apperrors — use case errors (ErrURLBlocked, ErrURLExpired)
//	interfaces/http       — HTTP errors (404, 410, 409) — translated here
//
// Why a separate package from domain errors?
//
//	Domain errors express violations of business invariants (a URL's short
//	code must be unique). Application errors express use case outcomes that
//	the HTTP layer needs to handle (a URL has expired → HTTP 410 Gone).
//	These are conceptually different: domain errors are about data integrity;
//	application errors are about user-visible outcomes.
//
// The HTTP handler performs the final translation:
//
//	apperrors.ErrNotFound  → 404 Not Found
//	apperrors.ErrURLExpired → 410 Gone
//	apperrors.ErrValidation → 422 Unprocessable Entity (RFC 7807)
//	apperrors.ErrConflict  → 409 Conflict
package apperrors

import (
	"errors"
	"fmt"
)

// Sentinel errors for use case outcomes.
// HTTP handlers use errors.Is() to map these to status codes.
var (
	// ErrNotFound is returned when the requested resource does not exist.
	// HTTP: 404 Not Found
	ErrNotFound = errors.New("app: resource not found")

	// ErrURLExpired is returned when a URL exists but has passed its expiry.
	// HTTP: 410 Gone (distinct from 404 — the resource existed but is gone)
	ErrURLExpired = errors.New("app: url has expired")

	// ErrURLDisabled is returned when a URL has been disabled by the owner.
	// HTTP: 404 Not Found (we don't leak that it exists but is disabled)
	ErrURLDisabled = errors.New("app: url is disabled")

	// ErrURLBlocked is returned when a URL fails the threat intelligence check.
	// HTTP: 422 Unprocessable Entity
	ErrURLBlocked = errors.New("app: url is blocked by safety policy")

	// ErrShortCodeConflict is returned when a custom short code already exists.
	// HTTP: 409 Conflict
	ErrShortCodeConflict = errors.New("app: short code already exists")

	// ErrUnauthorized is returned when the caller lacks permission for the operation.
	// HTTP: 403 Forbidden
	ErrUnauthorized = errors.New("app: unauthorized")

	// ErrUnauthenticated is returned when no valid credentials are provided.
	// HTTP: 401 Unauthorized
	ErrUnauthenticated = errors.New("app: unauthenticated")
)

// ValidationError carries structured validation failure information.
// It allows the HTTP handler to return RFC 7807 Problem Details responses
// with specific field-level error messages.
type ValidationError struct {
	// Message is the human-readable description of the validation failure.
	Message string

	// Cause is the underlying error that triggered the validation failure.
	// May be nil for top-level validation errors.
	Cause error
}

func (e *ValidationError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("validation error: %s: %v", e.Message, e.Cause)
	}
	return fmt.Sprintf("validation error: %s", e.Message)
}

func (e *ValidationError) Unwrap() error {
	return e.Cause
}

// NewValidationError constructs a ValidationError.
// Use nil for cause when there is no underlying error.
func NewValidationError(message string, cause error) *ValidationError {
	return &ValidationError{Message: message, Cause: cause}
}

// IsValidationError returns true if err is or wraps a *ValidationError.
// Used in tests and HTTP handlers.
func IsValidationError(err error) bool {
	var ve *ValidationError
	return errors.As(err, &ve)
}

// IsNotFound returns true if err is or wraps ErrNotFound.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

// IsConflict returns true if err is or wraps ErrShortCodeConflict.
func IsConflict(err error) bool {
	return errors.Is(err, ErrShortCodeConflict)
}

// Package response provides helpers for writing consistent JSON and
// RFC 7807 Problem Details responses from HTTP handlers.
//
// All API responses follow one of two structures:
//
//	Success:  {"data": {...}}                  (200, 201, 204)
//	Error:    RFC 7807 Problem Details object  (4xx, 5xx)
//
// Why RFC 7807?
//
//	Machine-readable error responses let API clients programmatically
//	distinguish error types without string-matching on "message" fields.
//	The "type" URI is a stable identifier that survives message rewordings.
//	This is what Stripe, GitHub, and Atlassian all use.
package response

import (
	"encoding/json"
	"net/http"
)

// Envelope wraps successful API responses.
// Every non-error response has a top-level "data" key.
// This reserves space for future envelope fields (pagination meta,
// request_id, warnings) without breaking existing clients.
type Envelope struct {
	Data any `json:"data"`
}

// Problem is an RFC 7807 Problem Details object.
// https://www.rfc-editor.org/rfc/rfc7807
type Problem struct {
	// Type is a URI that identifies the problem type.
	// Clients use this for programmatic error handling.
	// MUST be a stable URI — never change it once published.
	Type string `json:"type"`

	// Title is a short, human-readable summary of the problem type.
	// SHOULD NOT change between occurrences of the same problem.
	Title string `json:"title"`

	// Status is the HTTP status code (mirrored from the response status).
	// Included so clients can detect the status code from the body alone.
	Status int `json:"status"`

	// Detail is a human-readable explanation specific to this occurrence.
	// MAY change between occurrences. Use for user-facing messages.
	Detail string `json:"detail,omitempty"`

	// Instance is a URI reference identifying this specific occurrence.
	// Typically set to the request path.
	Instance string `json:"instance,omitempty"`

	// Errors provides field-level detail for validation errors (422).
	// Empty for non-validation errors.
	Errors []FieldError `json:"errors,omitempty"`
}

// FieldError carries per-field validation failure information.
// Returned in Problem.Errors for HTTP 422 responses.
type FieldError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Standard problem type URIs.
// These are stable identifiers — once published, never change them.
// Clients register handlers by type URI, not by status code.
const (
	ProblemTypeValidation      = "https://docs.shortener.example.com/errors/validation-error"
	ProblemTypeNotFound        = "https://docs.shortener.example.com/errors/not-found"
	ProblemTypeGone            = "https://docs.shortener.example.com/errors/gone"
	ProblemTypeConflict        = "https://docs.shortener.example.com/errors/conflict"
	ProblemTypeUnauthorized    = "https://docs.shortener.example.com/errors/unauthorized"
	ProblemTypeUnauthenticated = "https://docs.shortener.example.com/errors/unauthenticated"
	ProblemTypeInternal        = "https://docs.shortener.example.com/errors/internal-server-error"
	ProblemTypeURLBlocked      = "https://docs.shortener.example.com/errors/url-blocked"
)

// JSON writes a JSON-encoded response with the given status code.
// The Content-Type header is always set to application/json.
//
// Encoding errors are intentionally swallowed — by the time we call
// json.Encode, headers have already been written. We cannot change the
// status code. Logging is handled by the caller.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteProblem writes an RFC 7807 Problem Details response.
// Content-Type is set to application/problem+json per the RFC.
func WriteProblem(w http.ResponseWriter, p Problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// ── Convenience constructors for common error responses ──────────────────────

// NotFound writes a 404 Problem Details response.
func NotFound(w http.ResponseWriter, instance string) {
	WriteProblem(w, Problem{
		Type:     ProblemTypeNotFound,
		Title:    "Not Found",
		Status:   http.StatusNotFound,
		Detail:   "The requested resource was not found.",
		Instance: instance,
	})
}

// Gone writes a 410 Problem Details response.
// Used for expired URLs per PRD FR-URL-03.
func Gone(w http.ResponseWriter, instance string) {
	WriteProblem(w, Problem{
		Type:     ProblemTypeGone,
		Title:    "Gone",
		Status:   http.StatusGone,
		Detail:   "This short URL has expired and is no longer active.",
		Instance: instance,
	})
}

// Conflict writes a 409 Problem Details response.
func Conflict(w http.ResponseWriter, detail, instance string) {
	WriteProblem(w, Problem{
		Type:     ProblemTypeConflict,
		Title:    "Conflict",
		Status:   http.StatusConflict,
		Detail:   detail,
		Instance: instance,
	})
}

// InternalError writes a 500 Problem Details response.
// The detail message is intentionally generic — never expose
// internal error details (stack traces, query errors) to clients.
func InternalError(w http.ResponseWriter, instance string) {
	WriteProblem(w, Problem{
		Type:     ProblemTypeInternal,
		Title:    "Internal Server Error",
		Status:   http.StatusInternalServerError,
		Detail:   "An unexpected error occurred. Please try again later.",
		Instance: instance,
	})
}

// BadRequest writes a 400 Problem Details response.
func BadRequest(w http.ResponseWriter, detail, instance string) {
	WriteProblem(w, Problem{
		Type:     ProblemTypeValidation,
		Title:    "Bad Request",
		Status:   http.StatusBadRequest,
		Detail:   detail,
		Instance: instance,
	})
}

// UnprocessableEntity writes a 422 Problem Details response.
// Used for semantic validation failures (valid JSON, invalid business rules).
func UnprocessableEntity(w http.ResponseWriter, detail, instance string, fieldErrs ...FieldError) {
	WriteProblem(w, Problem{
		Type:     ProblemTypeValidation,
		Title:    "Validation Error",
		Status:   http.StatusUnprocessableEntity,
		Detail:   detail,
		Instance: instance,
		Errors:   fieldErrs,
	})
}

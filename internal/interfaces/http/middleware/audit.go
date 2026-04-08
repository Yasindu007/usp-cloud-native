package middleware

import (
	"context"
	"net/http"

	chimiddleware "github.com/go-chi/chi/v5/middleware"

	domainaudit "github.com/urlshortener/platform/internal/domain/audit"
)

// AuditCapturer is the interface for audit event building and capture.
// Implemented by audit.Service.
// Defined here at the consumer boundary.
type AuditCapturer interface {
	BuildEvent(
		ctx context.Context,
		action domainaudit.Action,
		resourceType domainaudit.ResourceType,
		resourceID string,
		sourceIP, userAgent, requestID string,
		metadata map[string]any,
	) *domainaudit.Event
	Capture(evt *domainaudit.Event)
}

// AuditAction returns a chi-compatible middleware that:
//  1. Starts a pending audit event and stores it in the request context
//  2. Calls the next handler
//  3. After the handler returns, reads any annotations added via
//     domainaudit.AnnotateContext() and ships the event to the audit service
//
// This "start then annotate then finalise" pattern means:
//   - Middleware sets up the event template (actor, IP, request ID)
//   - The handler annotates it with resource details (what was created)
//   - Middleware finalises and sends it
//
// Only successful write operations produce audit events. If the handler
// wrote a 4xx or 5xx response, the event is discarded — failed operations
// are captured by the error response itself (logs + metrics).
//
// Usage in router:
//
//	r.With(
//	    middleware.AuditAction(auditSvc, domainaudit.ActionURLCreate),
//	).Post("/urls", handler.Handle)
//
// Usage in handler (to add resource details):
//
//	domainaudit.AnnotateContext(r.Context(),
//	    domainaudit.ResourceURL, result.ID,
//	    map[string]any{"short_code": result.ShortCode})
func AuditAction(
	svc AuditCapturer,
	action domainaudit.Action,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Build a partial event template with request-level context.
			// ResourceType and ResourceID are unknown until the handler runs
			// (we don't know the created resource's ID before creation).
			evt := svc.BuildEvent(
				r.Context(),
				action,
				"", // ResourceType: set by handler via AnnotateContext
				"", // ResourceID:   set by handler via AnnotateContext
				r.RemoteAddr,
				r.UserAgent(),
				chimiddleware.GetReqID(r.Context()),
				nil, // Metadata:     set by handler via AnnotateContext
			)

			// Store the partial event in context so the handler can annotate it.
			ctx := domainaudit.WithPendingEvent(r.Context(), evt)

			// Wrap the response writer to capture the status code.
			rw := newStatusResponseWriter(w)

			// Execute the handler chain.
			next.ServeHTTP(rw, r.WithContext(ctx))

			// After handler returns: only capture audit event for success.
			// 2xx responses indicate a completed write operation.
			// 4xx: validation error / auth failure — not a completed operation.
			// 5xx: infrastructure error — business operation did not complete.
			//
			// Exception: ActionAuthFailed is explicitly for failed auth,
			// so we capture it regardless of status code.
			status := rw.Status()
			isSuccess := status >= 200 && status < 300
			isAuthFailure := action == domainaudit.ActionAuthFailed

			if !isSuccess && !isAuthFailure {
				return
			}

			// Retrieve any annotations the handler added (resource ID, metadata).
			if annotated, ok := domainaudit.PendingEventFromContext(ctx); ok {
				// Only ship events that have a resource ID — events without
				// one mean the handler didn't call AnnotateContext, which
				// means it's not a tracked operation (e.g. a read).
				if annotated.ResourceID != "" || isAuthFailure {
					svc.Capture(annotated)
				}
			}
		})
	}
}

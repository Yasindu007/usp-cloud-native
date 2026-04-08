package middleware

import (
	"context"
	"fmt"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	domainauth "github.com/urlshortener/platform/internal/domain/auth"
	domainworkspace "github.com/urlshortener/platform/internal/domain/workspace"
	"github.com/urlshortener/platform/internal/interfaces/http/response"
)

// MemberLookup is the interface for checking workspace membership.
// Implemented by postgres.WorkspaceRepository.
// Defined here at the consumer (middleware) to keep the dependency
// pointing inward.
type MemberLookup interface {
	GetMember(ctx context.Context, workspaceID, userID string) (*domainworkspace.Member, error)
}

// workspaceMemberKey is the context key for storing the member record.
type workspaceMemberKey struct{}

// WorkspaceAuth returns middleware that:
//  1. Reads the workspace_id from the authenticated user's JWT claims
//  2. Looks up the caller's membership record in that workspace
//  3. Stores the Member (including role) in the request context
//  4. Rejects non-members with 403 Forbidden
//
// Position in middleware chain:
//
//	The JWT Authenticate middleware MUST run before this one because
//	WorkspaceAuth reads claims populated by Authenticate.
//	Correct order: Authenticate → WorkspaceAuth → RequireAction → Handler
//
// Design: why middleware instead of in the handler?
//
//	Every protected endpoint needs the same check: "is the caller a member
//	of the workspace in their token?" Doing this in each handler is
//	boilerplate and easy to forget. The middleware enforces it uniformly.
//	The stored Member in context means handlers call MemberFromContext()
//	to get the role — zero additional DB queries per handler.
func WorkspaceAuth(lookup MemberLookup) func(http.Handler) http.Handler {
	tracer := otel.Tracer("github.com/urlshortener/platform/internal/interfaces/http/middleware")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Claims must exist (set by Authenticate middleware).
			claims, ok := domainauth.FromContext(r.Context())
			if !ok {
				response.WriteProblem(w, response.Problem{
					Type:   response.ProblemTypeUnauthenticated,
					Title:  "Unauthorized",
					Status: http.StatusUnauthorized,
					Detail: "Authentication required.",
				})
				return
			}

			ctx, span := tracer.Start(r.Context(), "WorkspaceAuth",
				trace.WithAttributes(
					attribute.String("workspace.id", claims.WorkspaceID),
					attribute.String("user.id", claims.UserID),
				),
			)
			defer span.End()

			// Look up the caller's membership.
			// Returns ErrMemberNotFound if the user is not in the workspace.
			member, err := lookup.GetMember(ctx, claims.WorkspaceID, claims.UserID)
			if err != nil {
				if domainworkspace.IsNotFound(err) {
					// User's token specifies a workspace they are not a member of.
					// Return 403 (not 404) — we confirm the workspace exists in their token,
					// but they have no access. 404 would be misleading.
					span.RecordError(err)
					response.WriteProblem(w, response.Problem{
						Type:   response.ProblemTypeUnauthorized,
						Title:  "Forbidden",
						Status: http.StatusForbidden,
						Detail: "You are not a member of this workspace.",
					})
					return
				}
				// Infrastructure error — fail closed (deny access).
				span.RecordError(err)
				response.InternalError(w, r.URL.Path)
				return
			}

			// Store the member record in context for handlers to use.
			ctx = context.WithValue(ctx, workspaceMemberKey{}, member)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// MemberFromContext retrieves the workspace Member from the request context.
// Returns (nil, false) if WorkspaceAuth middleware did not run.
func MemberFromContext(ctx context.Context) (*domainworkspace.Member, bool) {
	m, ok := ctx.Value(workspaceMemberKey{}).(*domainworkspace.Member)
	if !ok || m == nil {
		return nil, false
	}
	return m, true
}

// RequireAction returns middleware that checks the caller's workspace role
// permits the specified action. Must run after WorkspaceAuth.
//
// Usage:
//
//	r.With(RequireAction(domainworkspace.ActionCreateURL)).Post("/urls", handler)
func RequireAction(action domainworkspace.Action) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			member, ok := MemberFromContext(r.Context())
			if !ok {
				response.InternalError(w, r.URL.Path)
				return
			}

			if !member.Role.Can(action) {
				response.WriteProblem(w, response.Problem{
					Type:   response.ProblemTypeUnauthorized,
					Title:  "Forbidden",
					Status: http.StatusForbidden,
					Detail: fmt.Sprintf(
						"Your role (%s) does not permit this action (%s).",
						member.Role, action,
					),
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

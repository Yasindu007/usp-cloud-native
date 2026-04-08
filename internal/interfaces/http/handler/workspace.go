package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/urlshortener/platform/internal/application/apperrors"
	appworkspace "github.com/urlshortener/platform/internal/application/workspace"
	domainaudit "github.com/urlshortener/platform/internal/domain/audit"
	domainauth "github.com/urlshortener/platform/internal/domain/auth"
	"github.com/urlshortener/platform/internal/interfaces/http/response"
	"github.com/urlshortener/platform/pkg/logger"
)

// ── Use case interfaces ────────────────────────────────────────────────────────

type WorkspaceCreator interface {
	Handle(ctx context.Context, cmd appworkspace.CreateCommand) (*appworkspace.CreateResult, error)
}

type WorkspaceGetter interface {
	Handle(ctx context.Context, q appworkspace.GetQuery) (*appworkspace.GetResult, error)
}

type WorkspaceLister interface {
	Handle(ctx context.Context, q appworkspace.ListQuery) ([]*appworkspace.ListResult, error)
}

type MemberAdder interface {
	Handle(ctx context.Context, cmd appworkspace.AddMemberCommand) (*appworkspace.AddMemberResult, error)
}

type MemberLister interface {
	Handle(ctx context.Context, q appworkspace.ListMembersQuery) ([]*appworkspace.MemberResult, error)
}

// ── Request / Response types ──────────────────────────────────────────────────

type CreateWorkspaceRequest struct {
	Name string `json:"name"`
	Slug string `json:"slug,omitempty"`
}

type AddMemberRequest struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

// ── WorkspaceHandler ──────────────────────────────────────────────────────────

type WorkspaceHandler struct {
	creator      WorkspaceCreator
	getter       WorkspaceGetter
	lister       WorkspaceLister
	memberAdder  MemberAdder
	memberLister MemberLister
	log          *slog.Logger
}

func NewWorkspaceHandler(
	creator WorkspaceCreator, getter WorkspaceGetter, lister WorkspaceLister,
	memberAdder MemberAdder, memberLister MemberLister, log *slog.Logger,
) *WorkspaceHandler {
	return &WorkspaceHandler{
		creator: creator, getter: getter, lister: lister,
		memberAdder: memberAdder, memberLister: memberLister, log: log,
	}
}

// Create handles POST /api/v1/workspaces.
func (h *WorkspaceHandler) Create(w http.ResponseWriter, r *http.Request) {
	log := logger.FromContext(r.Context()).With(
		slog.String("handler", "WorkspaceHandler.Create"),
		slog.String("request_id", chimiddleware.GetReqID(r.Context())),
	)

	claims, ok := domainauth.FromContext(r.Context())
	if !ok {
		response.WriteProblem(w, response.Problem{
			Type: response.ProblemTypeUnauthenticated, Title: "Unauthorized",
			Status: http.StatusUnauthorized,
		})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req CreateWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "request body could not be decoded", r.URL.Path)
		return
	}

	result, err := h.creator.Handle(r.Context(), appworkspace.CreateCommand{
		Name:    req.Name,
		Slug:    req.Slug,
		OwnerID: claims.UserID,
	})
	if err != nil {
		h.writeError(w, r, err, log)
		return
	}

	// Audit annotation
	domainaudit.AnnotateContext(r.Context(),
		domainaudit.ResourceWorkspace,
		result.ID,
		map[string]any{"name": result.Name, "slug": result.Slug},
	)

	log.Info("workspace created", slog.String("id", result.ID), slog.String("slug", result.Slug))
	response.JSON(w, http.StatusCreated, response.Envelope{Data: result})
}

// Get handles GET /api/v1/workspaces/{workspaceID}.
func (h *WorkspaceHandler) Get(w http.ResponseWriter, r *http.Request) {
	claims, ok := domainauth.FromContext(r.Context())
	if !ok {
		response.WriteProblem(w, response.Problem{
			Type: response.ProblemTypeUnauthenticated, Title: "Unauthorized",
			Status: http.StatusUnauthorized,
		})
		return
	}
	workspaceID := chi.URLParam(r, "workspaceID")
	result, err := h.getter.Handle(r.Context(), appworkspace.GetQuery{
		WorkspaceID: workspaceID, RequestingUserID: claims.UserID,
	})
	if err != nil {
		h.writeError(w, r, err, logger.FromContext(r.Context()))
		return
	}
	response.JSON(w, http.StatusOK, response.Envelope{Data: result})
}

// List handles GET /api/v1/workspaces.
func (h *WorkspaceHandler) List(w http.ResponseWriter, r *http.Request) {
	claims, ok := domainauth.FromContext(r.Context())
	if !ok {
		response.WriteProblem(w, response.Problem{
			Type: response.ProblemTypeUnauthenticated, Title: "Unauthorized",
			Status: http.StatusUnauthorized,
		})
		return
	}
	results, err := h.lister.Handle(r.Context(), appworkspace.ListQuery{UserID: claims.UserID})
	if err != nil {
		h.writeError(w, r, err, logger.FromContext(r.Context()))
		return
	}
	response.JSON(w, http.StatusOK, response.Envelope{Data: results})
}

// AddMember handles POST /api/v1/workspaces/{workspaceID}/members.
func (h *WorkspaceHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	log := logger.FromContext(r.Context()).With(slog.String("handler", "WorkspaceHandler.AddMember"))

	claims, ok := domainauth.FromContext(r.Context())
	if !ok {
		response.WriteProblem(w, response.Problem{
			Type: response.ProblemTypeUnauthenticated, Title: "Unauthorized",
			Status: http.StatusUnauthorized,
		})
		return
	}

	workspaceID := chi.URLParam(r, "workspaceID")

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req AddMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "request body could not be decoded", r.URL.Path)
		return
	}

	result, err := h.memberAdder.Handle(r.Context(), appworkspace.AddMemberCommand{
		WorkspaceID:      workspaceID,
		InvitedUserID:    req.UserID,
		Role:             req.Role,
		RequestingUserID: claims.UserID,
	})
	if err != nil {
		h.writeError(w, r, err, log)
		return
	}

	// Audit annotation: member add affects the member resource
	domainaudit.AnnotateContext(r.Context(),
		domainaudit.ResourceMember,
		req.UserID, // the invited user is the resource
		map[string]any{
			"role":       result.Role,
			"invited_by": claims.UserID,
		},
	)

	response.JSON(w, http.StatusCreated, response.Envelope{Data: result})
}

// ListMembers handles GET /api/v1/workspaces/{workspaceID}/members.
func (h *WorkspaceHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	claims, ok := domainauth.FromContext(r.Context())
	if !ok {
		response.WriteProblem(w, response.Problem{
			Type: response.ProblemTypeUnauthenticated, Title: "Unauthorized",
			Status: http.StatusUnauthorized,
		})
		return
	}
	workspaceID := chi.URLParam(r, "workspaceID")
	results, err := h.memberLister.Handle(r.Context(), appworkspace.ListMembersQuery{
		WorkspaceID: workspaceID, RequestingUserID: claims.UserID,
	})
	if err != nil {
		h.writeError(w, r, err, logger.FromContext(r.Context()))
		return
	}
	response.JSON(w, http.StatusOK, response.Envelope{Data: results})
}

func (h *WorkspaceHandler) writeError(w http.ResponseWriter, r *http.Request, err error, log *slog.Logger) {
	var ve *apperrors.ValidationError
	if errors.As(err, &ve) {
		response.UnprocessableEntity(w, ve.Message, r.URL.Path)
		return
	}
	if errors.Is(err, apperrors.ErrUnauthorized) {
		response.WriteProblem(w, response.Problem{
			Type:   response.ProblemTypeUnauthorized,
			Title:  "Forbidden",
			Status: http.StatusForbidden,
			Detail: "You do not have permission to perform this action.",
		})
		return
	}
	if errors.Is(err, apperrors.ErrShortCodeConflict) {
		response.Conflict(w, "A workspace with that name or slug already exists.", r.URL.Path)
		return
	}
	log.Error("unexpected error in workspace handler",
		slog.String("error", err.Error()),
		slog.String("path", r.URL.Path),
	)
	response.InternalError(w, r.URL.Path)
}

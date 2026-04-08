package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	appkey "github.com/urlshortener/platform/internal/application/apikey"
	"github.com/urlshortener/platform/internal/application/apperrors"
	domainauth "github.com/urlshortener/platform/internal/domain/auth"
	"github.com/urlshortener/platform/internal/interfaces/http/response"
	"github.com/urlshortener/platform/pkg/logger"
)

// ── Use case interfaces ───────────────────────────────────────────────────────

type APIKeyCreator interface {
	Handle(ctx context.Context, cmd appkey.CreateCommand) (*appkey.CreateResult, error)
}

type APIKeyRevoker interface {
	Handle(ctx context.Context, cmd appkey.RevokeCommand) error
}

type APIKeyLister interface {
	Handle(ctx context.Context, q appkey.ListQuery) ([]*appkey.KeySummary, error)
}

// ── Request / Response types ──────────────────────────────────────────────────

// CreateAPIKeyRequest is the JSON body for POST /api-keys.
type CreateAPIKeyRequest struct {
	Name      string     `json:"name"`
	Scopes    []string   `json:"scopes"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// APIKeyHandler handles all API key HTTP endpoints.
type APIKeyHandler struct {
	creator APIKeyCreator
	revoker APIKeyRevoker
	lister  APIKeyLister
	log     *slog.Logger
}

// NewAPIKeyHandler constructs an APIKeyHandler.
func NewAPIKeyHandler(
	creator APIKeyCreator,
	revoker APIKeyRevoker,
	lister APIKeyLister,
	log *slog.Logger,
) *APIKeyHandler {
	return &APIKeyHandler{creator: creator, revoker: revoker, lister: lister, log: log}
}

// Create handles POST /api/v1/workspaces/{workspaceID}/api-keys
//
// Response MUST include the raw key in the "raw_key" field.
// After this response the raw key is gone — the user cannot retrieve it again.
// The response body includes a prominent "store_now" warning field to
// reinforce this to API clients.
func (h *APIKeyHandler) Create(w http.ResponseWriter, r *http.Request) {
	log := logger.FromContext(r.Context()).With(
		slog.String("handler", "APIKeyHandler.Create"),
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

	workspaceID := chi.URLParam(r, "workspaceID")

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req CreateAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "request body could not be decoded", r.URL.Path)
		return
	}

	result, err := h.creator.Handle(r.Context(), appkey.CreateCommand{
		WorkspaceID: workspaceID,
		Name:        req.Name,
		Scopes:      req.Scopes,
		ExpiresAt:   req.ExpiresAt,
		CreatedBy:   claims.UserID,
	})
	if err != nil {
		h.writeError(w, r, err, log)
		return
	}

	log.Info("api key created via http",
		slog.String("key_id", result.ID),
		slog.String("workspace_id", result.WorkspaceID),
		// NOTE: raw key is intentionally not logged
	)

	// Include a prominent warning in the response body.
	// This is best practice for services that issue long-lived secrets —
	// GitHub, Stripe, and AWS all include similar warnings.
	response.JSON(w, http.StatusCreated, response.Envelope{
		Data: map[string]any{
			"id":           result.ID,
			"name":         result.Name,
			"key_prefix":   result.KeyPrefix,
			"raw_key":      result.RawKey, // SHOW ONCE
			"store_now":    "This is the only time the key will be shown. Store it securely now.",
			"scopes":       result.Scopes,
			"workspace_id": result.WorkspaceID,
			"created_at":   result.CreatedAt,
			"expires_at":   result.ExpiresAt,
		},
	})
}

// List handles GET /api/v1/workspaces/{workspaceID}/api-keys
func (h *APIKeyHandler) List(w http.ResponseWriter, r *http.Request) {
	claims, ok := domainauth.FromContext(r.Context())
	if !ok {
		response.WriteProblem(w, response.Problem{
			Type: response.ProblemTypeUnauthenticated, Title: "Unauthorized",
			Status: http.StatusUnauthorized,
		})
		return
	}

	workspaceID := chi.URLParam(r, "workspaceID")

	results, err := h.lister.Handle(r.Context(), appkey.ListQuery{
		WorkspaceID:      workspaceID,
		RequestingUserID: claims.UserID,
	})
	if err != nil {
		h.writeError(w, r, err, logger.FromContext(r.Context()))
		return
	}

	response.JSON(w, http.StatusOK, response.Envelope{Data: results})
}

// Revoke handles DELETE /api/v1/workspaces/{workspaceID}/api-keys/{keyID}
func (h *APIKeyHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	log := logger.FromContext(r.Context()).With(
		slog.String("handler", "APIKeyHandler.Revoke"),
	)

	claims, ok := domainauth.FromContext(r.Context())
	if !ok {
		response.WriteProblem(w, response.Problem{
			Type: response.ProblemTypeUnauthenticated, Title: "Unauthorized",
			Status: http.StatusUnauthorized,
		})
		return
	}

	workspaceID := chi.URLParam(r, "workspaceID")
	keyID := chi.URLParam(r, "keyID")

	err := h.revoker.Handle(r.Context(), appkey.RevokeCommand{
		KeyID:            keyID,
		WorkspaceID:      workspaceID,
		RequestingUserID: claims.UserID,
	})
	if err != nil {
		h.writeError(w, r, err, log)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ── Error mapping ─────────────────────────────────────────────────────────────

func (h *APIKeyHandler) writeError(w http.ResponseWriter, r *http.Request, err error, log *slog.Logger) {
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
	if errors.Is(err, apperrors.ErrNotFound) {
		response.NotFound(w, r.URL.Path)
		return
	}
	log.Error("unexpected error in api key handler",
		slog.String("error", err.Error()),
		slog.String("path", r.URL.Path),
	)
	response.InternalError(w, r.URL.Path)
}

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
	appwebhook "github.com/urlshortener/platform/internal/application/webhook"
	domainaudit "github.com/urlshortener/platform/internal/domain/audit"
	domainauth "github.com/urlshortener/platform/internal/domain/auth"
	"github.com/urlshortener/platform/internal/interfaces/http/response"
	"github.com/urlshortener/platform/pkg/logger"
)

type WebhookRegistrar interface {
	Handle(ctx context.Context, cmd appwebhook.RegisterCommand) (*appwebhook.RegisterResult, error)
}

type WebhookLister interface {
	Handle(ctx context.Context, q appwebhook.ListQuery) ([]*appwebhook.WebhookSummary, error)
}

type WebhookDeleter interface {
	Handle(ctx context.Context, cmd appwebhook.DeleteCommand) error
}

type RegisterWebhookRequest struct {
	Name   string   `json:"name"`
	URL    string   `json:"url"`
	Events []string `json:"events"`
}

type WebhookHandler struct {
	registrar WebhookRegistrar
	lister    WebhookLister
	deleter   WebhookDeleter
	log       *slog.Logger
}

func NewWebhookHandler(registrar WebhookRegistrar, lister WebhookLister, deleter WebhookDeleter, log *slog.Logger) *WebhookHandler {
	return &WebhookHandler{registrar: registrar, lister: lister, deleter: deleter, log: log}
}

func (h *WebhookHandler) Register(w http.ResponseWriter, r *http.Request) {
	log := logger.FromContext(r.Context()).With(
		slog.String("handler", "WebhookHandler.Register"),
		slog.String("request_id", chimiddleware.GetReqID(r.Context())),
	)
	claims, ok := domainauth.FromContext(r.Context())
	if !ok {
		response.WriteProblem(w, response.Problem{Type: response.ProblemTypeUnauthenticated, Title: "Unauthorized", Status: http.StatusUnauthorized})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req RegisterWebhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "request body could not be decoded", r.URL.Path)
		return
	}

	result, err := h.registrar.Handle(r.Context(), appwebhook.RegisterCommand{
		WorkspaceID:      claims.WorkspaceID,
		RequestingUserID: claims.UserID,
		Name:             req.Name,
		URL:              req.URL,
		Events:           req.Events,
	})
	if err != nil {
		h.writeError(w, r, err, log)
		return
	}

	domainaudit.AnnotateContext(r.Context(), domainaudit.ResourceWebhook, result.ID, map[string]any{
		"name":   result.Name,
		"url":    result.URL,
		"events": result.Events,
	})

	response.JSON(w, http.StatusCreated, response.Envelope{
		Data: map[string]any{
			"id":           result.ID,
			"workspace_id": result.WorkspaceID,
			"name":         result.Name,
			"url":          result.URL,
			"secret":       result.Secret,
			"store_secret": "This is the only time the signing secret will be shown. Store it securely.",
			"events":       result.Events,
			"status":       result.Status,
			"created_at":   result.CreatedAt,
		},
	})
}

func (h *WebhookHandler) List(w http.ResponseWriter, r *http.Request) {
	claims, ok := domainauth.FromContext(r.Context())
	if !ok {
		response.WriteProblem(w, response.Problem{Type: response.ProblemTypeUnauthenticated, Title: "Unauthorized", Status: http.StatusUnauthorized})
		return
	}
	results, err := h.lister.Handle(r.Context(), appwebhook.ListQuery{
		WorkspaceID: claims.WorkspaceID,
		Limit:       parseIntQuery(r, "limit", 20, 100),
	})
	if err != nil {
		h.writeError(w, r, err, logger.FromContext(r.Context()))
		return
	}
	response.JSON(w, http.StatusOK, response.Envelope{Data: results})
}

func (h *WebhookHandler) Delete(w http.ResponseWriter, r *http.Request) {
	log := logger.FromContext(r.Context()).With(slog.String("handler", "WebhookHandler.Delete"))
	claims, ok := domainauth.FromContext(r.Context())
	if !ok {
		response.WriteProblem(w, response.Problem{Type: response.ProblemTypeUnauthenticated, Title: "Unauthorized", Status: http.StatusUnauthorized})
		return
	}
	webhookID := chi.URLParam(r, "webhookID")
	if err := h.deleter.Handle(r.Context(), appwebhook.DeleteCommand{
		WebhookID:        webhookID,
		WorkspaceID:      claims.WorkspaceID,
		RequestingUserID: claims.UserID,
	}); err != nil {
		h.writeError(w, r, err, log)
		return
	}
	domainaudit.AnnotateContext(r.Context(), domainaudit.ResourceWebhook, webhookID, map[string]any{"deleted_by": claims.UserID})
	w.WriteHeader(http.StatusNoContent)
}

func (h *WebhookHandler) writeError(w http.ResponseWriter, r *http.Request, err error, log *slog.Logger) {
	var ve *apperrors.ValidationError
	if errors.As(err, &ve) {
		response.UnprocessableEntity(w, ve.Message, r.URL.Path)
		return
	}
	if errors.Is(err, apperrors.ErrNotFound) {
		response.NotFound(w, r.URL.Path)
		return
	}
	log.Error("unexpected error in webhook handler", slog.String("error", err.Error()), slog.String("path", r.URL.Path))
	response.InternalError(w, r.URL.Path)
}

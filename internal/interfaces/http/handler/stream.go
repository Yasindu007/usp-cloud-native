package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	domainauth "github.com/urlshortener/platform/internal/domain/auth"
	domainurl "github.com/urlshortener/platform/internal/domain/url"
	redisinfra "github.com/urlshortener/platform/internal/infrastructure/redis"
	"github.com/urlshortener/platform/internal/interfaces/http/response"
	"github.com/urlshortener/platform/pkg/logger"
)

// StreamURLLookup resolves a workspace-scoped URL for per-URL stream authorization.
type StreamURLLookup interface {
	GetByID(ctx context.Context, id, workspaceID string) (*domainurl.URL, error)
}

// ClickStreamSubscriber creates click event subscriptions.
type ClickStreamSubscriber interface {
	SubscribeToURL(ctx context.Context, workspaceID, shortCode string) (redisinfra.ClickEventStream, error)
	SubscribeToWorkspace(ctx context.Context, workspaceID string) (redisinfra.ClickEventStream, error)
}

// StreamHandler serves SSE click streams.
type StreamHandler struct {
	urlRepo    StreamURLLookup
	subscriber ClickStreamSubscriber
	log        *slog.Logger
}

// NewStreamHandler creates a StreamHandler.
func NewStreamHandler(urlRepo StreamURLLookup, subscriber ClickStreamSubscriber, log *slog.Logger) *StreamHandler {
	return &StreamHandler{urlRepo: urlRepo, subscriber: subscriber, log: log}
}

// StreamWorkspace streams all non-bot clicks for the current workspace.
func (h *StreamHandler) StreamWorkspace(w http.ResponseWriter, r *http.Request) {
	claims, ok := domainauth.FromContext(r.Context())
	if !ok {
		response.WriteProblem(w, response.Problem{
			Type: response.ProblemTypeUnauthenticated, Title: "Unauthorized", Status: http.StatusUnauthorized,
		})
		return
	}

	sub, err := h.subscriber.SubscribeToWorkspace(r.Context(), claims.WorkspaceID)
	if err != nil {
		h.log.Error("workspace click stream subscribe failed", slog.String("error", err.Error()))
		response.InternalError(w, r.URL.Path)
		return
	}
	defer sub.Close()

	h.stream(w, r, sub)
}

// StreamURL streams non-bot clicks for one URL in the current workspace.
func (h *StreamHandler) StreamURL(w http.ResponseWriter, r *http.Request) {
	claims, ok := domainauth.FromContext(r.Context())
	if !ok {
		response.WriteProblem(w, response.Problem{
			Type: response.ProblemTypeUnauthenticated, Title: "Unauthorized", Status: http.StatusUnauthorized,
		})
		return
	}

	urlID := chi.URLParam(r, "urlID")
	u, err := h.urlRepo.GetByID(r.Context(), urlID, claims.WorkspaceID)
	if err != nil {
		if errors.Is(err, domainurl.ErrNotFound) || errors.Is(err, domainurl.ErrDeleted) {
			response.NotFound(w, r.URL.Path)
			return
		}
		h.log.Error("url stream lookup failed", slog.String("error", err.Error()))
		response.InternalError(w, r.URL.Path)
		return
	}

	sub, err := h.subscriber.SubscribeToURL(r.Context(), claims.WorkspaceID, u.ShortCode)
	if err != nil {
		h.log.Error("url click stream subscribe failed", slog.String("error", err.Error()))
		response.InternalError(w, r.URL.Path)
		return
	}
	defer sub.Close()

	h.stream(w, r, sub)
}

func (h *StreamHandler) stream(w http.ResponseWriter, r *http.Request, sub redisinfra.ClickEventStream) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		response.InternalError(w, r.URL.Path)
		return
	}

	log := logger.FromContext(r.Context()).With(slog.String("handler", "StreamHandler"))

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	if err := writeSSEEvent(w, "connected", map[string]string{"status": "connected"}); err != nil {
		log.Warn("failed to write SSE connected event", slog.String("error", err.Error()))
		return
	}
	flusher.Flush()

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	events := sub.Events()
	for {
		select {
		case <-r.Context().Done():
			return
		case evt, ok := <-events:
			if !ok {
				return
			}
			if evt == nil || evt.IsBot {
				continue
			}
			if err := writeSSEEvent(w, "click", evt); err != nil {
				log.Warn("failed to write SSE click event", slog.String("error", err.Error()))
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := w.Write([]byte(": heartbeat\n\n")); err != nil {
				log.Warn("failed to write SSE heartbeat", slog.String("error", err.Error()))
				return
			}
			flusher.Flush()
		}
	}
}

func writeSSEEvent(w http.ResponseWriter, event string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte("event: " + event + "\n")); err != nil {
		return err
	}
	if _, err := w.Write([]byte("data: " + string(payload) + "\n\n")); err != nil {
		return err
	}
	return nil
}

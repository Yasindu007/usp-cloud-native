package webhook

import (
	"context"
	"time"

	domainwebhook "github.com/urlshortener/platform/internal/domain/webhook"
)

type ListQuery struct {
	WorkspaceID string
	Limit       int
}

type WebhookSummary struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	URL           string     `json:"url"`
	Events        []string   `json:"events"`
	Status        string     `json:"status"`
	FailureCount  int        `json:"failure_count"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	LastFailureAt *time.Time `json:"last_failure_at,omitempty"`
	CreatedAt     string     `json:"created_at"`
}

type ListHandler struct {
	repo domainwebhook.Repository
}

func NewListHandler(repo domainwebhook.Repository) *ListHandler {
	return &ListHandler{repo: repo}
}

func (h *ListHandler) Handle(ctx context.Context, q ListQuery) ([]*WebhookSummary, error) {
	webhooks, err := h.repo.ListByWorkspace(ctx, q.WorkspaceID, q.Limit)
	if err != nil {
		return nil, err
	}
	out := make([]*WebhookSummary, 0, len(webhooks))
	for _, wh := range webhooks {
		out = append(out, &WebhookSummary{
			ID:            wh.ID,
			Name:          wh.Name,
			URL:           wh.URL,
			Events:        append([]string(nil), wh.Events...),
			Status:        string(wh.Status),
			FailureCount:  wh.FailureCount,
			LastSuccessAt: wh.LastSuccessAt,
			LastFailureAt: wh.LastFailureAt,
			CreatedAt:     wh.CreatedAt.Format(time.RFC3339),
		})
	}
	return out, nil
}

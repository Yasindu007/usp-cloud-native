package webhook

import (
	"strings"
	"time"
)

type EventType string

const (
	EventURLCreated       EventType = "url.created"
	EventURLUpdated       EventType = "url.updated"
	EventURLDeleted       EventType = "url.deleted"
	EventRedirectReceived EventType = "redirect.received"
)

var AllEventTypes = []EventType{
	EventURLCreated,
	EventURLUpdated,
	EventURLDeleted,
	EventRedirectReceived,
}

type Status string

const (
	StatusActive   Status = "active"
	StatusFailing  Status = "failing"
	StatusDisabled Status = "disabled"
)

type DeliveryStatus string

const (
	DeliveryPending    DeliveryStatus = "pending"
	DeliveryDelivering DeliveryStatus = "delivering"
	DeliveryDelivered  DeliveryStatus = "delivered"
	DeliveryFailed     DeliveryStatus = "failed"
	DeliveryAbandoned  DeliveryStatus = "abandoned"
)

const (
	MaxWebhooksPerWorkspace = 10
	MaxAttempts             = 5
)

var retrySchedule = []time.Duration{
	30 * time.Second,
	5 * time.Minute,
	30 * time.Minute,
	2 * time.Hour,
}

type Webhook struct {
	ID            string
	WorkspaceID   string
	Name          string
	URL           string
	Secret        string
	Events        []string
	Status        Status
	CreatedBy     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	LastSuccessAt *time.Time
	LastFailureAt *time.Time
	FailureCount  int
}

func (w *Webhook) IsSubscribed(eventType EventType) bool {
	for _, event := range w.Events {
		if event == string(eventType) {
			return true
		}
	}
	return false
}

func (w *Webhook) IsDeliverable() bool {
	return w.Status == StatusActive
}

type Delivery struct {
	ID             string
	WebhookID      string
	WorkspaceID    string
	EventType      EventType
	EventID        string
	Payload        []byte
	Status         DeliveryStatus
	AttemptCount   int
	NextAttemptAt  time.Time
	LastAttemptAt  *time.Time
	LastHTTPStatus *int
	LastError      string
	DeliveredAt    *time.Time
	CreatedAt      time.Time
}

type Event struct {
	Type        EventType
	EventID     string
	WorkspaceID string
	OccurredAt  time.Time
	Data        map[string]any
}

type EventPayload struct {
	ID          string         `json:"id"`
	EventType   EventType      `json:"event_type"`
	EventID     string         `json:"event_id"`
	WorkspaceID string         `json:"workspace_id"`
	OccurredAt  time.Time      `json:"occurred_at"`
	Data        map[string]any `json:"data"`
}

func IsValidEventType(s string) bool {
	for _, event := range AllEventTypes {
		if string(event) == s {
			return true
		}
	}
	return false
}

func ValidateURL(rawURL string) bool {
	lower := strings.ToLower(rawURL)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

func NextRetryDelay(attemptCount int) (time.Duration, bool) {
	if attemptCount <= 0 || attemptCount >= MaxAttempts {
		return 0, false
	}
	idx := attemptCount - 1
	if idx >= len(retrySchedule) {
		return 0, false
	}
	return retrySchedule[idx], true
}

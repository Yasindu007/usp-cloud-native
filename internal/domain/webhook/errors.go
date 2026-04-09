package webhook

import "errors"

var (
	ErrNotFound      = errors.New("webhook: not found")
	ErrLimitReached  = errors.New("webhook: workspace has reached the maximum of 10 webhooks")
	ErrInvalidURL    = errors.New("webhook: endpoint URL must use http or https scheme")
	ErrInvalidEvents = errors.New("webhook: one or more event types are invalid")
	ErrNameRequired  = errors.New("webhook: name is required")
	ErrURLRequired   = errors.New("webhook: url is required")
	ErrNoEvents      = errors.New("webhook: at least one event type must be subscribed")
)

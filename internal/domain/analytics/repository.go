package analytics

import "context"

// Repository defines persistence operations for redirect analytics.
type Repository interface {
	WriteMany(ctx context.Context, events []*RedirectEvent) error
	IncrementClickCounts(ctx context.Context, counts map[string]int64) error
}

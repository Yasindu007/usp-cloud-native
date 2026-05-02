package analytics

import "context"

// Repository defines the persistence contract for redirect events.
// Implemented by infrastructure/postgres.AnalyticsRepository.
//
// Write path design:
//
//	WriteMany is the primary write method. The ingestion service buffers
//	events and calls WriteMany in batches. Single-event Write is provided
//	for operational use (testing, low-volume writes) but is not called
//	from the hot path.
//
// Read path:
//
//	Read methods are defined here for future use by Story 3.2
//	(Analytics Aggregation API). They are NOT implemented in Story 3.1.
//	Defining the interface now prevents the Story 3.2 implementation from
//	needing to change the domain layer.
type Repository interface {
	// WriteMany inserts a batch of redirect events in a single round-trip.
	// Batch size is controlled by the ingestion service (default: 100 events).
	// Returns an error if the entire batch fails — no partial retry.
	WriteMany(ctx context.Context, events []*RedirectEvent) error

	// IncrementClickCounts atomically increments the denormalized click_count
	// on the urls table for each unique short_code in the batch.
	// Called alongside WriteMany — both in the same flush cycle.
	// Uses UPDATE ... SET click_count = click_count + N for efficiency.
	IncrementClickCounts(ctx context.Context, counts map[string]int64) error
}

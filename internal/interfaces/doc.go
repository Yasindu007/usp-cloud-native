// Package interfaces contains all driving adapters (primary adapters).
//
// These are the entry points through which external actors (HTTP clients,
// CLI tools, message queues) invoke the application use cases.
//
// Adapters in this package:
//   - http/       — chi HTTP router, handlers, middleware chain
//
// The HTTP handler's only job is:
//  1. Parse and validate the HTTP request (input deserialization)
//  2. Call the application use case handler
//  3. Serialize the result to HTTP response (output serialization)
//
// Handlers must never contain business logic.
// Business logic belongs in the application or domain layer.
//
// Populated in Story 1.5+ (shorten and redirect HTTP handlers).
package interfaces

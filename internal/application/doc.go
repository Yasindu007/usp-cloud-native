// Package application contains the use cases (application services) for the
// URL shortener platform.
//
// This layer orchestrates domain entities and infrastructure ports to fulfill
// user-facing commands and queries. It has no knowledge of HTTP, gRPC, or any
// delivery mechanism — that concern belongs to the interfaces layer.
//
// Pattern: CQRS (Command Query Responsibility Segregation)
//   - Commands: ShortenURLCommand, UpdateURLCommand, DeleteURLCommand
//   - Queries:  ResolveURLQuery, ListURLsQuery, GetURLQuery
//
// Each use case:
//   1. Validates inputs (at the application boundary)
//   2. Enforces authorization rules
//   3. Calls domain entity methods for business logic
//   4. Calls repository/cache ports for persistence
//   5. Returns a result or domain error
//
// Populated in Story 1.5+ (shorten, resolve use cases).
package application
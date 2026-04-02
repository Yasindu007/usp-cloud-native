// Package application contains the use cases (application services) for the
// URL shortener platform.
//
// Packages:
//
//	shorten/    — ShortenURL command handler (write path)
//	resolve/    — ResolveURL query handler  (read path, critical hot path)
//	apperrors/  — Application-layer error types for HTTP translation
//
// CQRS pattern:
//
//	Commands (shorten) mutate state and return a result.
//	Queries  (resolve) read state and return a result.
//	Neither knows about HTTP, gRPC, or any delivery mechanism.
//
// Dependency flow:
//
//	interfaces/http → application → domain
//	application uses domain interfaces (ports) only.
//	application never imports infrastructure packages directly.
package application

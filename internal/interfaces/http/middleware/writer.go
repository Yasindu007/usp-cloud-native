// Package middleware provides HTTP middleware components for the chi router.
// This file defines the shared statusResponseWriter used by both the
// logger and OTel middlewares to capture the HTTP response status code.
package middleware

import "net/http"

// statusResponseWriter wraps http.ResponseWriter to intercept and capture
// the response status code written by downstream handlers.
//
// Problem it solves:
//   http.ResponseWriter does not expose the written status code after the
//   fact. Middleware that needs to log or record the response status (like
//   a request logger or an OTel span recorder) must wrap the writer to
//   intercept the WriteHeader call.
//
// Thread safety:
//   This wrapper is not concurrent-safe but does not need to be — a single
//   HTTP request is served by a single goroutine in the standard library.
//
// Compatibility:
//   Only http.ResponseWriter is implemented. If the original writer
//   implements http.Flusher or http.Hijacker (for SSE/WebSocket),
//   callers should type-assert the original writer directly. For Phase 1
//   (REST only), this wrapper is sufficient.
type statusResponseWriter struct {
	http.ResponseWriter
	status  int
	written bool
}

// newStatusResponseWriter wraps an http.ResponseWriter.
// Default status is 200 — matches net/http behaviour when Write is called
// without a preceding WriteHeader.
func newStatusResponseWriter(w http.ResponseWriter) *statusResponseWriter {
	return &statusResponseWriter{
		ResponseWriter: w,
		status:         http.StatusOK,
	}
}

// WriteHeader captures the status code and delegates to the underlying writer.
// Once called, subsequent calls are no-ops (matches net/http semantics).
func (rw *statusResponseWriter) WriteHeader(code int) {
	if rw.written {
		return
	}
	rw.status = code
	rw.written = true
	rw.ResponseWriter.WriteHeader(code)
}

// Write ensures the status is recorded as 200 if WriteHeader was never
// called explicitly (standard library behaviour).
func (rw *statusResponseWriter) Write(b []byte) (int, error) {
	if !rw.written {
		rw.WriteHeader(http.StatusOK)
	}
	return rw.ResponseWriter.Write(b)
}

// Status returns the recorded HTTP status code.
func (rw *statusResponseWriter) Status() int {
	return rw.status
}

// Flush forwards streaming flushes to the underlying writer when supported.
// SSE handlers depend on this through middleware wrappers.
func (rw *statusResponseWriter) Flush() {
	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

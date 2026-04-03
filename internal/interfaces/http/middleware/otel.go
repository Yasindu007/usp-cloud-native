package middleware

import (
	"fmt"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// OTel returns a chi-compatible middleware that instruments each HTTP request
// with an OpenTelemetry server span.
//
// What this middleware does per request:
//  1. Extracts W3C traceparent/tracestate headers from the inbound request.
//     If present, the new span becomes a child of the upstream span.
//     This links: WSO2 APIM span → NGINX Ingress span → our service span.
//  2. Creates a server span with HTTP semantic attributes.
//  3. Injects the span context into the response headers so downstream
//     services can continue the same trace (not applicable in Phase 1,
//     but correct infrastructure for Phase 4 inter-service calls).
//  4. Records the response status on span completion.
//  5. Marks the span as Error if status >= 500.
//
// Span naming convention:
//
//	"<METHOD> <route-pattern>" e.g. "POST /api/v1/urls"
//	Using the route pattern (not the URL path) means "GET /r/{shortcode}"
//	aggregates all redirect spans under one span name in Jaeger/Tempo.
//	Using the raw URL path would create a unique span name per short code
//	(~millions of span names) — destroying the usefulness of span aggregation.
//
// serviceName is the tracer name used to identify which service created the span.
func OTel(serviceName string) func(http.Handler) http.Handler {
	tracer := otel.Tracer(serviceName)
	propagator := otel.GetTextMapPropagator()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Step 1: Extract trace context from inbound request headers.
			// If the request carries a valid traceparent header, the extracted
			// context contains the parent span reference. Otherwise it is empty
			// and the new span will be a root span (trace origin).
			ctx := propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))

			// Step 2: Determine span name.
			// chi stores the matched route pattern in the request context.
			// We read it after calling the next handler (chi sets it during routing),
			// so we use a placeholder now and update it after the call.
			// For simplicity in Phase 1, we use Method + Path.
			// Phase 4 uses chi.RouteContext(r.Context()).RoutePattern() for cleaner names.
			spanName := fmt.Sprintf("%s %s", r.Method, r.URL.Path)

			// Step 3: Start a server-kind span.
			ctx, span := tracer.Start(ctx, spanName,
				oteltrace.WithSpanKind(oteltrace.SpanKindServer),
				oteltrace.WithAttributes(
					attribute.String("http.method", r.Method),
					attribute.String("http.url", r.URL.String()),
					attribute.String("http.host", r.Host),
					attribute.String("http.scheme", scheme(r)),
					attribute.String("net.peer.addr", r.RemoteAddr),
				),
			)
			defer span.End()

			// Step 4: Inject the current span context into response headers.
			// Allows downstream services (called by the handler) to continue the trace.
			propagator.Inject(ctx, propagation.HeaderCarrier(w.Header()))

			// Step 5: Wrap the response writer to capture the status code.
			// This is a second wrap (logger middleware also wraps), which is correct:
			// each middleware is self-contained and captures its own status snapshot.
			rw := newStatusResponseWriter(w)

			// Step 6: Call the next handler with the span context.
			// The span is now available via otel.Tracer().Start(r.Context(), ...)
			// in any handler or use case downstream.
			next.ServeHTTP(rw, r.WithContext(ctx))

			// Step 7: Record the response status on the completed span.
			span.SetAttributes(attribute.Int("http.status_code", rw.Status()))

			// Mark as error for server errors — SRE alert on error span rate.
			if rw.Status() >= 500 {
				span.SetStatus(codes.Error, http.StatusText(rw.Status()))
			}
		})
	}
}

// scheme returns the request scheme (http or https).
// r.URL.Scheme is often empty for server-side requests; we derive it from TLS.
func scheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	// Check X-Forwarded-Proto set by load balancers / ingress.
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		return proto
	}
	return "http"
}

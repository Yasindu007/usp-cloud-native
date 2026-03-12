// Package telemetry initializes the OpenTelemetry SDK for distributed tracing.
//
// OpenTelemetry is the CNCF standard for vendor-neutral observability.
// Instrumentation is added once; backends (Jaeger, Tempo, Datadog) are
// swapped via configuration without code changes.
//
// Phase 1 supports two exporters:
//   - stdout: writes spans as JSON to stdout. Zero dependencies, good for
//     local development without running a collector.
//   - otlp: sends spans via gRPC to any OTLP-compatible collector
//     (Jaeger, Grafana Tempo, OpenTelemetry Collector).
//
// Phase 3+ will add metrics and log exporters using the same SDK.
package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
)

// Config holds all OpenTelemetry configuration.
// Populated from the application Config struct in main.go.
type Config struct {
	// Enabled controls whether tracing is active.
	// When false, a no-op tracer is installed — all trace calls are
	// zero-cost. Useful for unit tests.
	Enabled bool

	// Exporter selects the span exporter: "stdout" or "otlp".
	Exporter string

	// OTLPEndpoint is the gRPC endpoint for the OTLP exporter.
	// Format: "host:port" (e.g., "localhost:4317")
	OTLPEndpoint string

	// Resource attributes identify this service in traces.
	ServiceName    string
	ServiceVersion string
	Environment    string

	// SampleRate controls what fraction of traces are recorded.
	// 1.0 = 100% (use in dev/staging), 0.1 = 10% (use in production).
	SampleRate float64
}

// ShutdownFunc is returned by InitTracer. The caller must invoke it during
// graceful shutdown to flush buffered spans before the process exits.
// Spans not flushed are permanently lost — always call this with a timeout context.
type ShutdownFunc func(ctx context.Context) error

// InitTracer configures and registers the global OpenTelemetry TracerProvider.
// Returns a shutdown function that must be called on application exit.
//
// Error handling philosophy: OTel initialization failures are non-fatal in
// development (we log and continue), but fatal in production (we cannot
// operate without observability per our SRE requirements).
func InitTracer(ctx context.Context, cfg Config) (ShutdownFunc, error) {
	if !cfg.Enabled {
		// Install a no-op provider — all otel.Tracer() calls work but
		// produce no spans. This avoids nil pointer checks throughout the codebase.
		otel.SetTracerProvider(otel.GetTracerProvider())
		return func(context.Context) error { return nil }, nil
	}

	// Build the resource that describes this service instance.
	// These attributes appear on every span from this process.
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
			semconv.DeploymentEnvironment(cfg.Environment),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: creating resource: %w", err)
	}

	// Initialize the chosen span exporter.
	exporter, err := buildExporter(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("telemetry: building exporter: %w", err)
	}

	// Select sampler based on sample rate.
	// AlwaysSample in dev, TraceIDRatioBased in production.
	var sampler sdktrace.Sampler
	if cfg.SampleRate >= 1.0 {
		sampler = sdktrace.AlwaysSample()
	} else {
		sampler = sdktrace.TraceIDRatioBased(cfg.SampleRate)
	}

	// The TracerProvider is the entry point to the OTel SDK.
	// BatchSpanProcessor is used (not Sync) so span export never blocks
	// the critical request path — spans are buffered and sent in background batches.
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	)

	// Register as the global provider. All otel.Tracer("name") calls
	// anywhere in the codebase (including library code) use this provider.
	otel.SetTracerProvider(tp)

	return tp.Shutdown, nil
}

// buildExporter constructs the appropriate span exporter based on configuration.
func buildExporter(ctx context.Context, cfg Config) (sdktrace.SpanExporter, error) {
	switch cfg.Exporter {
	case "otlp":
		// OTLP gRPC exporter — sends spans to any OTLP-compatible backend.
		// Uses insecure connection for local development.
		// In production: use TLS by configuring OTEL_EXPORTER_OTLP_CERTIFICATE.
		exporter, err := otlptracegrpc.New(ctx,
			otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint),
			otlptracegrpc.WithInsecure(), // TLS added in Phase 4
		)
		if err != nil {
			return nil, fmt.Errorf("creating otlp grpc exporter (endpoint=%s): %w", cfg.OTLPEndpoint, err)
		}
		return exporter, nil

	default: // "stdout"
		// Stdout exporter writes JSON-formatted spans to stdout.
		// Useful for local development without running a collector.
		// PrettyPrint makes them readable during debugging.
		exporter, err := stdouttrace.New(
			stdouttrace.WithPrettyPrint(),
		)
		if err != nil {
			return nil, fmt.Errorf("creating stdout exporter: %w", err)
		}
		return exporter, nil
	}
}

// Tracer returns a named tracer from the global provider.
// Convention: use the fully-qualified package path as the tracer name.
// This makes the origin of each span visible in trace UIs.
//
// Usage:
//
//	tracer := telemetry.Tracer("github.com/urlshortener/platform/internal/application/shorten")
//	ctx, span := tracer.Start(ctx, "ShortenURL")
//	defer span.End()
func Tracer(name string) trace.Tracer {
	return otel.Tracer(name)
}
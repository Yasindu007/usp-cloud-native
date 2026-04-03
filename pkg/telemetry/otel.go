// Package telemetry initializes the OpenTelemetry SDK for distributed tracing.
// Updated in Story 1.5 to also configure the global W3C Trace Context propagator.
// This enables the OTel HTTP middleware to extract traceparent headers from
// inbound requests and inject them into outbound ones, linking spans across
// service boundaries.
package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

// Config holds all OpenTelemetry configuration.
type Config struct {
	Enabled        bool
	Exporter       string // "stdout" | "otlp"
	OTLPEndpoint   string // host:port for OTLP gRPC
	ServiceName    string
	ServiceVersion string
	Environment    string
	SampleRate     float64 // 0.0–1.0
}

// ShutdownFunc must be called during graceful shutdown to flush buffered spans.
type ShutdownFunc func(ctx context.Context) error

// InitTracer configures and registers the global OpenTelemetry TracerProvider
// and the global W3C Trace Context propagator.
//
// The propagator is critical for distributed tracing across service boundaries:
//   - Inbound: the OTel HTTP middleware extracts traceparent from request headers
//     so spans from the redirect-service become children of spans from WSO2/APIM.
//   - Outbound: any http.Client calls (added later) automatically inject the
//     traceparent header so downstream services continue the same trace.
//
// Without setting the global propagator, all services start fresh traces —
// you get isolated spans per service instead of end-to-end traces.
func InitTracer(ctx context.Context, cfg Config) (ShutdownFunc, error) {
	if !cfg.Enabled {
		// No-op provider: all otel.Tracer() calls work but produce no spans.
		// Avoids nil-check boilerplate throughout the codebase.
		otel.SetTracerProvider(otel.GetTracerProvider())
		return func(context.Context) error { return nil }, nil
	}

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

	exporter, err := buildExporter(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("telemetry: building exporter: %w", err)
	}

	var sampler sdktrace.Sampler
	if cfg.SampleRate >= 1.0 {
		sampler = sdktrace.AlwaysSample()
	} else {
		sampler = sdktrace.TraceIDRatioBased(cfg.SampleRate)
	}

	// BatchSpanProcessor exports spans asynchronously — never blocks request path.
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	)

	// Register as the global TracerProvider.
	// All otel.Tracer("name") calls anywhere in the codebase use this.
	otel.SetTracerProvider(tp)

	// Register the W3C Trace Context propagator as the global propagator.
	// CompositeTextMapPropagator chains multiple propagators:
	//   TraceContext: handles traceparent + tracestate headers (W3C standard)
	//   Baggage:      handles baggage header (key-value pairs across services)
	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		),
	)

	return tp.Shutdown, nil
}

func buildExporter(ctx context.Context, cfg Config) (sdktrace.SpanExporter, error) {
	switch cfg.Exporter {
	case "otlp":
		exp, err := otlptracegrpc.New(ctx,
			otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint),
			otlptracegrpc.WithInsecure(),
		)
		if err != nil {
			return nil, fmt.Errorf("creating otlp grpc exporter: %w", err)
		}
		return exp, nil
	default: // "stdout"
		exp, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
		if err != nil {
			return nil, fmt.Errorf("creating stdout exporter: %w", err)
		}
		return exp, nil
	}
}

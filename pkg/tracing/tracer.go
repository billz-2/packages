package tracing

import (
	"context"

	"github.com/pkg/errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	"go.opentelemetry.io/otel/trace"
)

type Config struct {
	ServiceName string `mapstructure:"serviceName"`
	JaegerUrl   string `mapstructure:"jaegerUrl"`
}

var tracer trace.Tracer

func GetGlobalTracer() trace.Tracer {
	if tracer == nil {
		tracer = otel.Tracer("default")
	}
	return tracer
}

func init() {
	_, _ = NewTraceProvider(context.Background(), &Config{})
}

func NewTraceProvider(ctx context.Context, jaegerConfig *Config) (*sdktrace.TracerProvider, error) {
	exp, err := newExporter(ctx, jaegerConfig)
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize exporter")
	}

	res := newResource(jaegerConfig)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)

	otel.SetTracerProvider(tp)

	tracer = tp.Tracer(jaegerConfig.ServiceName)

	return tp, nil
}

func newExporter(ctx context.Context, jaegerConfig *Config) (sdktrace.SpanExporter, error) {
	// In OpenTelemetry v1.35.0, the endpoint must be in host:port format without scheme or path
	// and needs proper timeout configuration
	url := jaegerConfig.JaegerUrl

	// Remove scheme (http:// or https://) if present
	if idx := indexOf(url, "://"); idx >= 0 {
		url = url[idx+3:]
	}

	// Remove path if present - for gRPC we only need host:port
	if idx := indexOf(url, "/"); idx >= 0 {
		url = url[:idx]
	}

	// Create exporter with properly formatted endpoint and increased timeout
	return otlptracegrpc.New(
		ctx,
		otlptracegrpc.WithEndpoint(url),
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithTimeout(30000000000), // 30 seconds
	)
}

func newResource(jaegerConfig *Config) *resource.Resource {
	return resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceNameKey.String(jaegerConfig.ServiceName),
	)
}

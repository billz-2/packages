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
	return otlptracegrpc.New(
		ctx,
		otlptracegrpc.WithEndpointURL(jaegerConfig.JaegerUrl),
		otlptracegrpc.WithInsecure(),
	)
}

func newResource(jaegerConfig *Config) *resource.Resource {
	return resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceNameKey.String(jaegerConfig.ServiceName),
	)
}

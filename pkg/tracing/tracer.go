package tracing

import (
	"github.com/pkg/errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/jaeger"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"go.opentelemetry.io/otel/trace"
)

type Config struct {
	ServiceName string `mapstructure:"serviceName"`
	JaegerUrl   string `mapstructure:"jaegerUrl"`
	JaegerHost  string `mapstructure:"jaegerHost"`
	JaegerPort  string `mapstructure:"jaegerPort"`
	Enable      bool   `mapstructure:"enable"`
	LogSpans    bool   `mapstructure:"logSpans"`
}

var tracer trace.Tracer

func GetGlobalTracer() trace.Tracer {
	if tracer == nil {
		tracer = otel.Tracer("default")
	}
	return tracer
}

func init() {
	_, _ = NewTraceProvider(&Config{})
}

func newExporter(jaegerConfig *Config) (*jaeger.Exporter, error) {
	exporter, err := jaeger.New(jaeger.WithCollectorEndpoint(jaeger.WithEndpoint(jaegerConfig.JaegerUrl)))
	if err != nil {
		return nil, errors.Wrap(err, "failed to create the Jaeger exporter")
	}

	return exporter, nil
}

func newTraceProvider(exp sdktrace.SpanExporter, jaegerConfig *Config) (*sdktrace.TracerProvider, error) {
	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceNameKey.String(jaegerConfig.ServiceName),
	)

	return sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	), nil
}

func NewTraceProvider(jaegerConfig *Config) (*sdktrace.TracerProvider, error) {
	exp, err := newExporter(jaegerConfig)
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize exporter")
	}

	tp, err := newTraceProvider(exp, jaegerConfig)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create the trace provider")
	}

	otel.SetTracerProvider(tp)

	tracer = tp.Tracer(jaegerConfig.ServiceName)

	return tp, nil
}

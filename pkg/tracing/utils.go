package tracing

import (
	"context"
	"fmt"

	"github.com/IBM/sarama"
	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// CustomCarrier implements the TextMapCarrier interface.
type CustomCarrier map[string]string

// Get implements the Get method of the TextMapCarrier interface.
func (cc CustomCarrier) Get(key string) string {
	return cc[key]
}

// Set implements the Set method of the TextMapCarrier interface.
func (cc CustomCarrier) Set(key string, value string) {
	cc[key] = value
}

func (cc CustomCarrier) Keys() []string {
	res := make([]string, 0)
	for k := range cc {
		res = append(res, k)
	}

	return res
}

func StartHttpServerTracerSpan(c *gin.Context, operationName string) (context.Context, trace.Span) {
	propagator := propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
	ctx := propagator.Extract(c.Request.Context(), propagation.HeaderCarrier(c.Request.Header))

	return GetGlobalTracer().Start(ctx, operationName)
}

func StartKafkaConsumerTracerSpan(ctx context.Context, headers []sarama.RecordHeader, operationName string) (context.Context, trace.Span) {
	var propagator = propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)

	carrierFromKafkaHeaders := textMapCarrierFromKafkaMessageHeaders(headers)

	ctx = propagator.Extract(ctx, carrierFromKafkaHeaders)

	return GetGlobalTracer().Start(ctx, operationName)
}

func GetEventHeaders(event cloudevents.Event) []sarama.RecordHeader {
	headers := make([]sarama.RecordHeader, 0)
	for k, v := range event.Extensions() {
		if k == "traceid" {
			k = "uber-trace-id"
		}
		headers = append(headers, sarama.RecordHeader{
			Key:   []byte(k),
			Value: []byte(v.(string)),
		})
	}
	return headers
}

func InjectDataToSpanAndEvent(ctx context.Context, event *cloudevents.Event, span trace.Span) {
	addEventLogsToSpan(event, span)

	headers := getKafkaTracingHeadersFromSpanCtx(ctx, span.SpanContext())
	for _, header := range headers {
		event.SetExtension(string(header.Key), string(header.Value))

	}
}

func InjectHeadersIntoCloudevents(event *cloudevents.Event, headers []sarama.RecordHeader) {
	for _, header := range headers {
		event.SetExtension(string(header.Key), string(header.Value))
	}
}

func GetKafkaTracingHeadersFromSpanCtx(ctx context.Context, spanCtx trace.SpanContext) []sarama.RecordHeader {
	textMapCarrier := injectTextMapCarrier(ctx, spanCtx)

	kafkaMessageHeaders := textMapCarrierToKafkaMessageHeaders(textMapCarrier)

	return kafkaMessageHeaders
}

func GetSpanIDFromContext(ctx context.Context) string {
	span := trace.SpanFromContext(ctx)
	span.SpanContext()

	return span.SpanContext().SpanID().String()
}

func GetTraceIDFromContext(ctx context.Context) string {
	span := trace.SpanFromContext(ctx)
	span.SpanContext()

	return span.SpanContext().TraceID().String()
}

func GetSpan(ctx context.Context, operationName string, req ...interface{}) (context.Context, trace.Span) {
	ctx, childSpan := GetGlobalTracer().Start(ctx, operationName)

	if len(req) > 0 {
		childSpan.SetAttributes(attribute.String("request", fmt.Sprintf("%+v", req[0])))
	}

	return ctx, childSpan
}

func textMapCarrierFromKafkaMessageHeaders(headers []sarama.RecordHeader) propagation.TextMapCarrier {
	textMap := CustomCarrier{}

	for _, header := range headers {
		textMap.Set(string(header.Key), string(header.Value))

	}
	return textMap
}

func injectTextMapCarrier(ctx context.Context, spanCtx trace.SpanContext) propagation.TextMapCarrier {
	var propagator = propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
	m := CustomCarrier{}

	propagator.Inject(ctx, propagation.TextMapCarrier(m))

	return m
}

func getKafkaTracingHeadersFromSpanCtx(ctx context.Context, spanCtx trace.SpanContext) []sarama.RecordHeader {
	textMapCarrier := injectTextMapCarrier(ctx, spanCtx)

	kafkaMessageHeaders := textMapCarrierToKafkaMessageHeaders(textMapCarrier)

	return kafkaMessageHeaders
}

func textMapCarrierToKafkaMessageHeaders(textMap propagation.TextMapCarrier) []sarama.RecordHeader {
	headers := make([]sarama.RecordHeader, 0, len(textMap.Keys()))

	for _, key := range textMap.Keys() {
		headers = append(headers, sarama.RecordHeader{
			Key:   []byte(key),
			Value: []byte(textMap.Get(key)),
		})
	}

	return headers
}

func addEventLogsToSpan(event *cloudevents.Event, span trace.Span) {
	span.SetAttributes(
		attribute.String("event", fmt.Sprintf("%v", event)),
	)
}

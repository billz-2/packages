package event

import (
	"context"
	"errors"
	"fmt"

	"github.com/IBM/sarama"
	"github.com/billz-2/packages/pkg/logger"
	"github.com/billz-2/packages/pkg/tracing"
	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/cloudevents/sdk-go/v2/event"
	"go.opentelemetry.io/otel/attribute"
)

type HandlerFunc func(context.Context, cloudevents.Event) Response

// Consumer ...
type Consumer struct {
	consumerName      string
	topic             string
	handler           HandlerFunc
	isSeparateRoutine bool
}

// AddConsumer ...
func (kafka *Kafka) AddConsumer(topic string, handler HandlerFunc, inSeparateRoutine ...bool) {
	if kafka.consumers[topic] != nil {
		panic(errors.New("consumer with the same name already exists: " + topic))
	}

	isSeparateRoutine := false
	if len(inSeparateRoutine) > 0 {
		isSeparateRoutine = inSeparateRoutine[0]
	}

	kafka.consumers[topic] = &Consumer{
		consumerName:      topic,
		topic:             topic,
		handler:           handler,
		isSeparateRoutine: isSeparateRoutine,
	}
}

// Setup is run at the beginning of a new session, before ConsumeClaim.
func (kafka *Kafka) Setup(_ sarama.ConsumerGroupSession) error {
	close(kafka.ready)
	return nil
}

// Cleanup is run at the end of a session, once all ConsumeClaim goroutines have exited
// but before the offsets are committed for the very last time.
func (kafka *Kafka) Cleanup(_ sarama.ConsumerGroupSession) error {
	return nil
}

// ConsumeClaim must start a consumer loop of ConsumerGroupClaim's Messages().
// Once the Messages() channel is closed, the Handler must finish its processing
// loop and exit.
func (kafka *Kafka) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	consumer := kafka.consumers[claim.Topic()]
	for {
		select {
		case message := <-claim.Messages():
			if message == nil {
				logger.Log.Error(
					"ConsumeClaim",
					logger.Any("consumer", consumer),
					logger.Any("topic", claim.Topic()),
				)
				continue
			}
			e := MessageToEvent(message)

			// long-running consumers should run in separate go routine,
			// because they can affect the whole consumer group to go down
			if consumer.isSeparateRoutine {
				go kafka.handleMessage(consumer, e)
			} else {
				kafka.handleMessage(consumer, e)
			}

			// Check if the context is canceled before marking the message
			select {
			case <-session.Context().Done():
				return nil // Context is canceled, return without marking the message
			default:
				session.MarkMessage(message, "")
			}
		case <-session.Context().Done():
			return nil
		}
	}
}

func (kafka *Kafka) handleMessage(consumer *Consumer, event event.Event) {
	ctx, span := tracing.StartKafkaConsumerTracerSpan(kafka.ctx, tracing.GetEventHeaders(event), "Kafka.ConsumeClaim")
	defer span.End()

	span.SetAttributes(attribute.String("event", event.String()), attribute.String("request", string(event.DataEncoded)))

	resp := consumer.handler(ctx, event)
	if resp.Topic == "" {
		return
	}

	span.SetAttributes(attribute.String("response", fmt.Sprintf("%+v", resp)))

	err := event.SetData(cloudevents.ApplicationJSON, resp)
	tracing.InjectDataToSpanAndEvent(ctx, &event, span)
	if err != nil {
		logger.Log.Error("Failed to set data", logger.Any("error:", err))
		return
	}

	err = kafka.Push(resp.Topic, event)
	if err != nil {
		logger.Log.Error("Failed to push", logger.Any("error:", err), logger.Any("topic", resp.Topic))
		return
	}
}

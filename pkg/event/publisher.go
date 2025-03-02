package event

import (
	"context"
	"errors"
	"fmt"

	"github.com/IBM/sarama"
	"github.com/billz-2/packages/pkg/logger"
	"github.com/cloudevents/sdk-go/protocol/kafka_sarama/v2"
	cloudevents "github.com/cloudevents/sdk-go/v2"
)

// Publisher ...
type Publisher struct {
	topic            string
	cloudEventClient cloudevents.Client
	sender           *kafka_sarama.Sender
}

// AddPublisher ...
func (kafka *Kafka) AddPublisher(topic string) {
	if kafka.publishers[topic] != nil {
		logger.Log.Warn("publisher exists", logger.Error(errors.New("publisher with the same topic already exists: "+topic)))
		return
	}

	sender, err := kafka_sarama.NewSender(
		[]string{kafka.cfg.KafkaUrl}, // Kafka connection url
		kafka.saramaConfig,           // Kafka sarama config
		topic,                        // Topic
	)

	if err != nil {
		panic(err)
	}

	// defer sender.Close(context.Background())

	c, err := cloudevents.NewClient(sender, cloudevents.WithTimeNow(), cloudevents.WithUUIDs())
	if err != nil {
		panic(err)
	}

	kafka.publishers[topic] = &Publisher{
		topic:            topic,
		cloudEventClient: c,
		sender:           sender,
	}
}

// Push ...
func (kafka *Kafka) Push(topic string, e cloudevents.Event) error {
	p := kafka.publishers[topic]

	if p == nil {
		return fmt.Errorf("publisher with that topic doesn't exists: %s", topic)
	}

	result := p.cloudEventClient.Send(
		kafka_sarama.WithMessageKey(context.Background(), sarama.StringEncoder(e.ID())),
		e,
	)

	if cloudevents.IsUndelivered(result) {
		return fmt.Errorf("failed to publish event, Error: %v", result)
	}

	return nil
}

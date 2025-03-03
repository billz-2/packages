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
func (k *kafka) AddPublisher(topic string) {
	if k.publishers[topic] != nil {
		logger.Log.Warn("publisher exists", logger.Error(errors.New("publisher with the same topic already exists: "+topic)))
		return
	}

	sender, err := kafka_sarama.NewSender(
		[]string{k.cfg.BootstrapURL}, // Kafka connection url
		k.saramaConfig,               // Kafka sarama config
		topic,                        // Topic
	)

	if err != nil {
		panic(err)
	}

	c, err := cloudevents.NewClient(sender, cloudevents.WithTimeNow(), cloudevents.WithUUIDs())
	if err != nil {
		panic(err)
	}

	k.publishers[topic] = &Publisher{
		topic:            topic,
		cloudEventClient: c,
		sender:           sender,
	}
}

// Push sends and event to a topic with specific partition key
// TODO uncelar what to do in IsUndelivered case
func (k *kafka) Push(topic string, e cloudevents.Event, partitionID string) (err error) {
	p := k.publishers[topic]

	if p == nil {
		return fmt.Errorf("publisher with that topic doesn't exists: %s", topic)
	}

	key := k.getPassedPartitionID(e, partitionID)

	e.SetExtension(PartitionKey, key)

	result := p.cloudEventClient.Send(
		kafka_sarama.WithMessageKey(context.Background(), sarama.StringEncoder(fmt.Sprint(key))),
		e,
	)

	if cloudevents.IsUndelivered(result) {
		return fmt.Errorf("failed to publish event: %v", result)
	}

	return nil
}

// getPassedPartitionID returns passed Partition ID
// The order is following:
// 1. check directly passed ID
// 2. extract ID from extention
// 3. by default return ID of event as partition ID
func (k *kafka) getPassedPartitionID(e cloudevents.Event, partitionID string) interface{} {
	if partitionID != "" {
		return partitionID
	}

	partitionKeyExtention, err := e.Context.GetExtension(PartitionKey)
	if err != nil {
		return e.ID()
	}

	return partitionKeyExtention

}

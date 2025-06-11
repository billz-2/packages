package usereventlog

import (
	cloudevents "github.com/cloudevents/sdk-go/v2"
)

type Kafka interface {
	Push(topic string, e cloudevents.Event, partitionKey string) error
}

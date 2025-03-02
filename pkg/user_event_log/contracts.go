package user_event_log

import cloudevents "github.com/cloudevents/sdk-go/v2"

type Kafka interface {
	Push(topic string, e cloudevents.Event) error
}

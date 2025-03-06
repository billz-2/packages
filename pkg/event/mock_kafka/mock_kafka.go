package mock_kafka

import (
	"context"
	"github.com/billz-2/packages/pkg/event"
	"sync"

	cloudevents "github.com/cloudevents/sdk-go/v2"
)

type MockKafka struct {
	Queue map[string][]cloudevents.Event
}

var mutex = &sync.RWMutex{}

type MockKafkaI interface {
	event.Kafka

	ResetQueue()
	GetQueue(string) ([]cloudevents.Event, bool)
}

func NewMockKafka() MockKafkaI {
	var mockKafka MockKafka

	queue := make(map[string][]cloudevents.Event)
	mockKafka = MockKafka{
		Queue: queue,
	}

	return &mockKafka
}

func (mk *MockKafka) ResetQueue() {
	mutex.Lock()
	mk.Queue = make(map[string][]cloudevents.Event)
	mutex.Unlock()
}

func (mk *MockKafka) AddConsumer(topic string, handler event.HandlerFunc, inSeparateRoutine ...bool) {
}

func (mk *MockKafka) AddPublisher(topic string) {
}

func (mk *MockKafka) Push(topic string, event cloudevents.Event, partitionKey string) error {
	mutex.Lock()
	mk.Queue[topic] = append(mk.Queue[topic], event)
	mutex.Unlock()
	return nil
}

func (mk *MockKafka) RegisterPublishers() {
}

func (mk *MockKafka) RunConsumers() {
}

func (mk *MockKafka) Shutdown(ctx context.Context) error {
	return nil
}

func (mk *MockKafka) GetQueue(topic string) ([]cloudevents.Event, bool) {
	events, ok := mk.Queue[topic]
	return events, ok
}

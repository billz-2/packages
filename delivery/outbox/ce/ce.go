// Package ce writes cloudevents to the outbox as the records the cloudevents kafka_sarama sender would send.
package ce

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/IBM/sarama"
	"github.com/cloudevents/sdk-go/protocol/kafka_sarama/v2"
	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/cloudevents/sdk-go/v2/binding"
	"github.com/cloudevents/sdk-go/v2/client"

	"github.com/billz-2/packages/delivery/outbox"
)

// Options configure a Publisher. Their zero values are the defaults.
type Options struct {
	// PartitionKeyExtension is the event extension that carries the record key; empty sets none.
	PartitionKeyExtension string
	// MaxMessageBytes is the producer's Producer.MaxMessageBytes. Default: sarama's.
	MaxMessageBytes int
	// WriteTimeout bounds a write without a transaction: it waits for a connection of the pool. Default 30s.
	WriteTimeout time.Duration
}

const defaultWriteTimeout = 30 * time.Second

// recordVersion is the record format sarama sizes a message with for Kafka 0.11 and later.
const recordVersion = 2

type Publisher struct {
	ob   *outbox.Outbox
	opts Options

	mu     sync.RWMutex
	topics map[string]struct{}
}

func NewPublisher(ob *outbox.Outbox, opts Options) *Publisher {
	if opts.MaxMessageBytes == 0 {
		opts.MaxMessageBytes = sarama.NewConfig().Producer.MaxMessageBytes
	}
	if opts.WriteTimeout == 0 {
		opts.WriteTimeout = defaultWriteTimeout
	}

	return &Publisher{ob: ob, opts: opts, topics: make(map[string]struct{})}
}

// AddTopic registers a topic here and with the outbox; Push refuses a topic that was not added. It returns
// false for a topic added before, whose options it leaves as they were.
func (p *Publisher) AddTopic(topic string, opts ...outbox.TopicOption) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.topics[topic]; ok {
		return false
	}
	p.topics[topic] = struct{}{}
	p.ob.Topic(topic, opts...)

	return true
}

func (p *Publisher) Topics() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	topics := make([]string, 0, len(p.topics))
	for topic := range p.topics {
		topics = append(topics, topic)
	}
	slices.Sort(topics)

	return topics
}

// Push writes the event to the outbox at once; the relay sends it from there.
func (p *Publisher) Push(ctx context.Context, topic string, e cloudevents.Event, key string) error {
	return p.PushTx(ctx, nil, topic, e, key)
}

// PushTx writes the event in tx, so that it is sent only if tx commits; with a nil tx it is Push. An empty key
// is the event id.
func (p *Publisher) PushTx(ctx context.Context, tx outbox.Execer, topic string, e cloudevents.Event, key string) error {
	msg, err := p.encode(ctx, topic, e, key)
	if err != nil {
		return &lastingError{err: err}
	}

	if tx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.opts.WriteTimeout)
		defer cancel()
	}

	return p.ob.Publish(ctx, tx, msg)
}

type lastingError struct{ err error }

func (e *lastingError) Error() string { return e.err.Error() }
func (e *lastingError) Unwrap() error { return e.err }

// IsLasting reports an error of Push or PushTx that writing again does not fix: a topic that was not added, an
// event that does not encode, a record over the producer's limit.
func IsLasting(err error) bool {
	var lasting *lastingError

	return errors.As(err, &lasting)
}

func (p *Publisher) encode(ctx context.Context, topic string, e cloudevents.Event, key string) (outbox.Message, error) {
	p.mu.RLock()
	_, ok := p.topics[topic]
	p.mu.RUnlock()
	if !ok {
		return outbox.Message{}, fmt.Errorf("ce: topic %s was not added", topic)
	}

	if key == "" {
		key = e.ID()
	}
	if p.opts.PartitionKeyExtension != "" {
		e.SetExtension(p.opts.PartitionKeyExtension, key)
	}

	msg, err := Encode(ctx, topic, e, key)
	if err != nil {
		return outbox.Message{}, err
	}

	// the producer refuses a record over Producer.MaxMessageBytes before it reaches the brokers; checked here
	// so that Push fails at once, not after the caller went on as if it was published
	if size := msg.ProducerMessage().ByteSize(recordVersion); size > p.opts.MaxMessageBytes {
		return outbox.Message{}, sarama.ConfigurationError(fmt.Sprintf(
			"Attempt to produce message larger than configured Producer.MaxMessageBytes: %d > %d",
			size, p.opts.MaxMessageBytes))
	}

	return msg, nil
}

// Encode builds the record the cloudevents kafka_sarama sender would send for e.
func Encode(ctx context.Context, topic string, e cloudevents.Event, key string) (outbox.Message, error) {
	e = client.DefaultTimeToNowIfNotSet(ctx, e)
	e = client.DefaultIDToUUIDIfNotSet(ctx, e)
	if err := e.Validate(); err != nil {
		return outbox.Message{}, err
	}

	pm := sarama.ProducerMessage{Topic: topic}
	if err := kafka_sarama.WriteProducerMessage(ctx, (*binding.EventMessage)(&e), &pm); err != nil {
		return outbox.Message{}, err
	}

	msg := outbox.Message{Topic: topic, Key: key, Headers: make([]outbox.Header, 0, len(pm.Headers))}
	if pm.Value != nil {
		value, err := pm.Value.Encode()
		if err != nil {
			return outbox.Message{}, err
		}
		msg.Value = value
	}
	for _, header := range pm.Headers {
		msg.Headers = append(msg.Headers, outbox.Header{Key: string(header.Key), Value: header.Value})
	}

	return msg, nil
}

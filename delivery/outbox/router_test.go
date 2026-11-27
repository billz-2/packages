package outbox_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/IBM/sarama"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/billz-2/packages/delivery/outbox"
)

type recordingProducer struct {
	mu     sync.Mutex
	topics []string
	fail   map[string]error
	// an error that names no record
	failAll error
}

func (p *recordingProducer) SendMessages(msgs []*sarama.ProducerMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.failAll != nil {
		return p.failAll
	}

	var errs sarama.ProducerErrors
	for _, msg := range msgs {
		if err := p.fail[msg.Topic]; err != nil {
			errs = append(errs, &sarama.ProducerError{Msg: msg, Err: err})

			continue
		}
		p.topics = append(p.topics, msg.Topic)
	}
	if len(errs) > 0 {
		return errs
	}

	return nil
}

func failedTopics(t *testing.T, err error) map[string]error {
	t.Helper()

	var errs sarama.ProducerErrors
	require.ErrorAs(t, err, &errs)
	failed := make(map[string]error)
	for _, perr := range errs {
		failed[perr.Msg.Topic] = perr.Err
	}

	return failed
}

func TestRouter_ReturnsEveryFailedRecord(t *testing.T) {
	boom := errors.New("boom")
	common := &recordingProducer{fail: map[string]error{"bad": sarama.ErrNotLeaderForPartition}}
	heavy := &recordingProducer{failAll: boom}
	producer := outbox.Router(func(topic string) outbox.Producer {
		switch topic {
		case "heavy":
			return heavy
		case "nowhere":
			return nil
		}

		return common
	})

	err := producer.SendMessages([]*sarama.ProducerMessage{
		{Topic: "good"}, {Topic: "bad"}, {Topic: "heavy"}, {Topic: "nowhere"},
	})

	failed := failedTopics(t, err)
	assert.Equal(t, sarama.ErrNotLeaderForPartition, failed["bad"])
	assert.Equal(t, boom, failed["heavy"])
	require.Error(t, failed["nowhere"])
	assert.Contains(t, failed["nowhere"].Error(), "no producer for topic nowhere")
	assert.Len(t, failed, 3)
	assert.Equal(t, []string{"good"}, common.topics)
}

func TestRouter_SendsEachGroupThroughItsProducer(t *testing.T) {
	a, b := &recordingProducer{}, &recordingProducer{}
	producer := outbox.Router(func(topic string) outbox.Producer {
		if topic == "b" {
			return b
		}

		return a
	})

	require.NoError(t, producer.SendMessages([]*sarama.ProducerMessage{{Topic: "a"}, {Topic: "b"}, {Topic: "a2"}}))
	assert.Equal(t, []string{"a", "a2"}, a.topics)
	assert.Equal(t, []string{"b"}, b.topics)
}

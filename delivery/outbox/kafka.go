package outbox

import (
	"errors"
	"fmt"
	"strings"

	"github.com/IBM/sarama"
)

// defaultRetryMax is sarama's own default, which an idempotent producer needs to be at least 1.
const defaultRetryMax = 3

// ProducerConfig sets what the relay's guarantee needs on the config of its clients: a row is deleted only
// after every in-sync replica has the record, and sarama's own retries cannot write it twice.
func ProducerConfig(c *sarama.Config) {
	c.Producer.RequiredAcks = sarama.WaitForAll
	c.Producer.Idempotent = true
	c.Net.MaxOpenRequests = 1
	if c.Producer.Retry.Max < 1 {
		c.Producer.Retry.Max = defaultRetryMax
	}
	c.Producer.Return.Successes = true
}

// NewProducer is sarama.NewSyncProducerFromClient for a client whose config has ProducerConfig's settings; it
// refuses any other.
func NewProducer(client sarama.Client) (sarama.SyncProducer, error) {
	if err := checkProducerConfig(client.Config()); err != nil {
		return nil, err
	}

	return sarama.NewSyncProducerFromClient(client)
}

func checkProducerConfig(c *sarama.Config) error {
	var missing []string
	if c.Producer.RequiredAcks != sarama.WaitForAll {
		missing = append(missing, "Producer.RequiredAcks = WaitForAll")
	}
	if !c.Producer.Idempotent {
		missing = append(missing, "Producer.Idempotent")
	}
	if c.Net.MaxOpenRequests != 1 {
		missing = append(missing, "Net.MaxOpenRequests = 1")
	}
	if c.Producer.Retry.Max < 1 {
		missing = append(missing, "Producer.Retry.Max > 0")
	}
	if len(missing) > 0 {
		return fmt.Errorf("outbox: the producer's client lacks %s (outbox.ProducerConfig)", strings.Join(missing, ", "))
	}
	if !c.Producer.Return.Successes {
		return errors.New("outbox: the producer's client lacks Producer.Return.Successes (outbox.ProducerConfig)")
	}

	return nil
}

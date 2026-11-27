package outbox

import (
	"errors"
	"sync"

	"github.com/IBM/sarama"
)

// Router sends each record through the producer that route picks for its topic, the groups of one call at
// once, and returns the failed records of all of them. route returns nil for a topic it cannot send, and the
// same comparable value (a pointer) for the topics of one producer: the groups are keyed by it.
func Router(route func(topic string) Producer) Producer {
	return router(route)
}

type router func(topic string) Producer

func (route router) SendMessages(msgs []*sarama.ProducerMessage) error {
	var (
		mu   sync.Mutex
		errs sarama.ProducerErrors
		wg   sync.WaitGroup
	)

	groups := make(map[Producer][]*sarama.ProducerMessage)
	for _, msg := range msgs {
		producer := route(msg.Topic)
		if producer == nil {
			errs = append(errs, &sarama.ProducerError{
				Msg: msg, Err: errors.New("outbox: no producer for topic " + msg.Topic),
			})

			continue
		}
		groups[producer] = append(groups[producer], msg)
	}

	for producer, group := range groups {
		wg.Add(1)
		go func() {
			defer wg.Done()

			err := producer.SendMessages(group)
			if err == nil {
				return
			}

			var failed sarama.ProducerErrors
			if !errors.As(err, &failed) {
				failed = make(sarama.ProducerErrors, 0, len(group))
				for _, msg := range group {
					failed = append(failed, &sarama.ProducerError{Msg: msg, Err: err})
				}
			}

			mu.Lock()
			errs = append(errs, failed...)
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(errs) > 0 {
		return errs
	}

	return nil
}

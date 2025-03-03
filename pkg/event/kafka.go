package event

import (
	"context"
	"fmt"
	"sync"

	// "go_boilerplate/pkg/logger"

	"github.com/IBM/sarama"
	"github.com/billz-2/packages/pkg/logger"
	cloudevents "github.com/cloudevents/sdk-go/v2"
)

type kafka struct {
	ctx           context.Context
	cfg           Config
	consumers     map[string]*Consumer
	publishers    map[string]*Publisher
	saramaConfig  *sarama.Config
	consumerGroup sarama.ConsumerGroup
	ready         chan struct{}
	wg            *sync.WaitGroup
}

type Kafka interface {
	RunConsumers()
	AddConsumer(topic string, handler HandlerFunc, inSeparateRoutine ...bool)
	Push(topic string, e cloudevents.Event, partitionKey string) error
	AddPublisher(topic string)
	Shutdown(ctx context.Context) error
}

func NewManagedKafkaGCP(ctx context.Context, cfg Config) (Kafka, error) {
	saramaConfig := initSaramaConfig(ctx, cfg)

	consumerGroup, err := sarama.NewConsumerGroup([]string{cfg.BootstrapURL}, cfg.ConsumerGroupID, saramaConfig)
	if err != nil {
		return nil, err
	}

	return &kafka{
		ctx:           ctx,
		cfg:           cfg,
		consumers:     make(map[string]*Consumer),
		publishers:    make(map[string]*Publisher),
		saramaConfig:  saramaConfig,
		ready:         make(chan struct{}),
		wg:            &sync.WaitGroup{},
		consumerGroup: consumerGroup,
	}, nil
}

// RunConsumers ...
func (r *kafka) RunConsumers() {
	topics := []string{}

	for _, consumer := range r.consumers {
		topics = append(topics, consumer.topic)
		fmt.Println("Key:", consumer.topic, "=>", "consumer:", consumer)
	}

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		for {
			if err := r.consumerGroup.Consume(r.ctx, topics, r); err != nil {
				logger.Log.Error("error while consuming", logger.Error(err))
			}
			if r.ctx.Err() != nil {
				return
			}
			r.ready = make(chan struct{})
		}
	}()

	<-r.ready
	logger.Log.Warn("consumer group started")
}

// TODO Gracefull shutdown ???
func (r *kafka) Shutdown(ctx context.Context) error {
	logger.Log.Warn("shutting down pub-sub server")
	select {
	case <-r.ctx.Done():
		logger.Log.Warn("terminating: context cancelled")
	default:
	}
	r.wg.Wait()
	r.consumerGroup.Close()

	for _, publisher := range r.publishers {
		if err := publisher.sender.Close(context.Background()); err != nil {
			logger.Log.Error("could not close sender", logger.Any("topic", publisher.topic), logger.Error(err))
		}
	}

	return nil
}

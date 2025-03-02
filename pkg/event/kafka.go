package event

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"github.com/IBM/sarama"
	"github.com/billz-2/packages/pkg/logger"
	cloudevents "github.com/cloudevents/sdk-go/v2"
)

type Kafka struct {
	ctx           context.Context
	log           logger.Logger
	cfg           KafkaConfig
	consumers     map[string]*Consumer
	publishers    map[string]*Publisher
	saramaConfig  *sarama.Config
	consumerGroup sarama.ConsumerGroup
	ready         chan struct{}
	wg            *sync.WaitGroup
}

type KafkaI interface {
	RunConsumers()
	AddConsumer(topic string, handler HandlerFunc, inSeparateRoutine ...bool)
	Push(topic string, e cloudevents.Event) error
	AddPublisher(topic string)
	Shutdown() error
}

func NewKafka(ctx context.Context, cfg KafkaConfig, log logger.Logger) (KafkaI, error) {
	saramaConfig := sarama.NewConfig()
	saramaConfig.Version = sarama.V2_0_0_0
	saramaConfig.Consumer.Offsets.Initial = sarama.OffsetOldest
	saramaConfig.Consumer.Group.Heartbeat.Interval = time.Second * 30
	saramaConfig.Consumer.Group.Session.Timeout = time.Second * 90
	saramaConfig.Consumer.Group.Rebalance.Timeout = time.Second * 60
	saramaConfig.Producer.MaxMessageBytes = 1024 * 1024 * 40
	saramaConfig.Consumer.MaxProcessingTime = time.Second * 60

	consumerGroup, err := sarama.NewConsumerGroup([]string{cfg.KafkaUrl}, cfg.ConsumerGroupID, saramaConfig)
	if err != nil {
		return nil, err
	}

	kafka := &Kafka{
		ctx:           ctx,
		log:           log,
		cfg:           cfg,
		consumers:     make(map[string]*Consumer),
		publishers:    make(map[string]*Publisher),
		saramaConfig:  saramaConfig,
		ready:         make(chan struct{}),
		wg:            &sync.WaitGroup{},
		consumerGroup: consumerGroup,
	}

	return kafka, nil
}

func NewManagedKafka(ctx context.Context, cfg KafkaConfig, log logger.Logger) (KafkaI, error) {
	saramaConfig := sarama.NewConfig()
	saramaConfig.Version = sarama.V3_0_0_0
	saramaConfig.Metadata.AllowAutoTopicCreation = true
	saramaConfig.Consumer.Offsets.Initial = sarama.OffsetOldest
	saramaConfig.Consumer.Group.Heartbeat.Interval = time.Second * 30
	saramaConfig.Consumer.Group.Session.Timeout = time.Second * 90
	saramaConfig.Consumer.Group.Rebalance.Timeout = time.Second * 60
	saramaConfig.Producer.MaxMessageBytes = 1024 * 1024 * 40
	saramaConfig.Consumer.MaxProcessingTime = time.Second * 60

	var url, certBase64, username, password string
	url = cfg.KafkaUrl
	certBase64 = cfg.KafkaCert
	username = cfg.KafkaUserName
	password = cfg.KafkaPassword

	if username != "" && password != "" {
		saramaConfig.Net.SASL.Enable = true
		saramaConfig.Net.SASL.User = username
		saramaConfig.Net.SASL.Password = password
		saramaConfig.Net.SASL.Handshake = true
		saramaConfig.Net.SASL.Mechanism = sarama.SASLTypePlaintext
		saramaConfig.Net.TLS.Enable = true
	}
	if certBase64 != "" {
		certBytes, err := base64.StdEncoding.DecodeString(certBase64)
		if err != nil {
			log.Fatal("Error while decoding cert", logger.Error(err))
		}
		log.Info("Certificate decoded successfully")
		caCertPool := x509.NewCertPool()
		caCertPool.AppendCertsFromPEM(certBytes)
		tlsConfig := &tls.Config{
			RootCAs: caCertPool,
		}
		saramaConfig.Net.TLS.Config = tlsConfig
		saramaConfig.Net.TLS.Enable = true
	}

	consumerGroup, err := sarama.NewConsumerGroup([]string{url}, cfg.ConsumerGroupID, saramaConfig)
	if err != nil {
		return nil, err
	}

	kafka := &Kafka{
		ctx:           ctx,
		log:           log,
		cfg:           cfg,
		consumers:     make(map[string]*Consumer),
		publishers:    make(map[string]*Publisher),
		saramaConfig:  saramaConfig,
		ready:         make(chan struct{}),
		wg:            &sync.WaitGroup{},
		consumerGroup: consumerGroup,
	}

	return kafka, nil
}

// RunConsumers ...
func (kafka *Kafka) RunConsumers() {
	topics := []string{}

	for _, consumer := range kafka.consumers {
		topics = append(topics, consumer.topic)
		fmt.Println("Key:", consumer.topic, "=>", "consumer:", consumer)
	}
	logger.Log.Info("topics:", logger.Any("topics:", topics))

	kafka.wg.Add(1)
	go func() {
		defer kafka.wg.Done()
		for {
			if err := kafka.consumerGroup.Consume(kafka.ctx, topics, kafka); err != nil {
				logger.Log.Error("error while consuming", logger.Error(err))
			}
			if kafka.ctx.Err() != nil {
				return
			}
			kafka.ready = make(chan struct{})
		}
	}()

	<-kafka.ready
	logger.Log.Warn("consumer group started")
}

func (kafka *Kafka) Shutdown() error {
	logger.Log.Warn("shutting down pub-sub server")
	select {
	case <-kafka.ctx.Done():
		logger.Log.Warn("terminating: context cancelled")
	default:
	}
	kafka.wg.Wait()
	kafka.consumerGroup.Close()

	for _, publisher := range kafka.publishers {
		if err := publisher.sender.Close(context.Background()); err != nil {
			logger.Log.Error("could not close sender", logger.Any("topic", publisher.topic), logger.Error(err))
		}
	}

	return nil
}

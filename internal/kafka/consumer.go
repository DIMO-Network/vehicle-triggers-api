package kafka

import (
	"context"
	"fmt"

	"github.com/IBM/sarama"
	"github.com/ThreeDotsLabs/watermill"
	wmkafka "github.com/ThreeDotsLabs/watermill-kafka/v3/pkg/kafka"
	"github.com/ThreeDotsLabs/watermill/message"
)

type ProcessorFunc func(ctx context.Context, messages <-chan *message.Message, maxInFlight int) error

type Config struct {
	ClusterConfig   *sarama.Config
	BrokerAddresses []string
	Topic           string
	GroupID         string
	MaxInFlight     int64
	Processor       ProcessorFunc
}

type Consumer struct {
	subscriber  *wmkafka.Subscriber
	topic       string
	Processor   ProcessorFunc
	maxInFlight int
}

func NewConsumer(cfg *Config) (*Consumer, error) {
	saramaSubscriberConfig := wmkafka.DefaultSaramaSubscriberConfig()

	saramaSubscriberConfig.Version = cfg.ClusterConfig.Version
	saramaSubscriberConfig.Consumer.Offsets.Initial = cfg.ClusterConfig.Consumer.Offsets.Initial

	subscriber, err := wmkafka.NewSubscriber(
		wmkafka.SubscriberConfig{
			Brokers:               cfg.BrokerAddresses,
			Unmarshaler:           wmkafka.DefaultMarshaler{},
			OverwriteSaramaConfig: saramaSubscriberConfig,
			ConsumerGroup:         cfg.GroupID,
		},
		watermill.NewStdLogger(false, false),
	)
	if err != nil {
		return nil, err
	}

	maxInFlight := int(cfg.MaxInFlight)
	if maxInFlight < 1 {
		maxInFlight = 1
	}

	return &Consumer{
		subscriber:  subscriber,
		topic:       cfg.Topic,
		Processor:   cfg.Processor,
		maxInFlight: maxInFlight,
	}, nil
}

func (c *Consumer) Start(ctx context.Context) error {
	messages, err := c.subscriber.Subscribe(ctx, c.topic)
	if err != nil {
		return fmt.Errorf("could not subscribe to topic: %s", c.topic)
	}
	if c.Processor == nil {
		return fmt.Errorf("processor function is nil")
	}

	return c.Processor(ctx, messages, c.maxInFlight)
}

func (c *Consumer) Stop(ctx context.Context) error {
	return c.subscriber.Close()
}

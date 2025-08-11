package kafka

import (
	"context"
	"fmt"

	"github.com/IBM/sarama"
	"github.com/ThreeDotsLabs/watermill"
	wm_kafka "github.com/ThreeDotsLabs/watermill-kafka/v3/pkg/kafka"
	"github.com/ThreeDotsLabs/watermill/message"
)

type Config struct {
	ClusterConfig   *sarama.Config
	BrokerAddresses []string
	Topic           string
	GroupID         string
	MaxInFlight     int64
}

type Consumer struct {
	subscriber *wm_kafka.Subscriber
	topic      string
}

func NewConsumer(cfg *Config) (*Consumer, error) {
	saramaSubscriberConfig := wm_kafka.DefaultSaramaSubscriberConfig()

	saramaSubscriberConfig.Version = cfg.ClusterConfig.Version
	saramaSubscriberConfig.Consumer.Offsets.Initial = cfg.ClusterConfig.Consumer.Offsets.Initial

	subscriber, err := wm_kafka.NewSubscriber(
		wm_kafka.SubscriberConfig{
			Brokers:               cfg.BrokerAddresses,
			Unmarshaler:           wm_kafka.DefaultMarshaler{},
			OverwriteSaramaConfig: saramaSubscriberConfig,
			ConsumerGroup:         cfg.GroupID,
		},
		watermill.NewStdLogger(false, false),
	)
	if err != nil {
		return nil, err
	}

	return &Consumer{
		subscriber: subscriber,
		topic:      cfg.Topic,
	}, nil
}

func (c *Consumer) Start(ctx context.Context, process func(ctx context.Context, messages <-chan *message.Message)) error {
	messages, err := c.subscriber.Subscribe(ctx, c.topic)
	if err != nil {
		return fmt.Errorf("could not subscribe to topic: %s", c.topic)
	}

	go process(ctx, messages)
	return nil
}

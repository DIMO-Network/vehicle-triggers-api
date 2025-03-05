package kafka

import (
	"context"

	"github.com/IBM/sarama"
	"github.com/ThreeDotsLabs/watermill"
	wm_kafka "github.com/ThreeDotsLabs/watermill-kafka/v3/pkg/kafka"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/rs/zerolog"
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
	logger     *zerolog.Logger
}

func NewConsumer(cfg *Config, logger *zerolog.Logger) (*Consumer, error) {
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
		logger:     logger,
	}, nil
}

func (c *Consumer) Start(ctx context.Context, process func(messages <-chan *message.Message)) {
	messages, err := c.subscriber.Subscribe(ctx, c.topic)
	if err != nil {
		c.logger.Fatal().Err(err).Msgf("Could not subscribe to topic: %s", c.topic)
	}

	go process(messages)
}

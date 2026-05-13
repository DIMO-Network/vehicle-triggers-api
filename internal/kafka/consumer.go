package kafka

import (
	"context"
	"fmt"

	"github.com/IBM/sarama"
	"github.com/ThreeDotsLabs/watermill"
	wmkafka "github.com/ThreeDotsLabs/watermill-kafka/v3/pkg/kafka"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/rs/zerolog"
)

type ProcessorFunc func(ctx context.Context, messages <-chan *message.Message, maxInFlight int) error

type Config struct {
	ClusterConfig   *sarama.Config
	BrokerAddresses []string
	Topic           string
	GroupID         string
	MaxInFlight     int64
	Processor       ProcessorFunc
	// Name is a human-readable identifier used in log lines (e.g. "signals", "events").
	Name string
}

type Consumer struct {
	subscriber  *wmkafka.Subscriber
	topic       string
	name        string
	Processor   ProcessorFunc
	maxInFlight int
}

// Name returns the consumer's identifier used in log lines.
func (c *Consumer) Name() string { return c.name }

// Topic returns the topic this consumer subscribes to.
func (c *Consumer) Topic() string { return c.topic }

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

	name := cfg.Name
	if name == "" {
		name = cfg.Topic
	}

	return &Consumer{
		subscriber:  subscriber,
		topic:       cfg.Topic,
		name:        name,
		Processor:   cfg.Processor,
		maxInFlight: maxInFlight,
	}, nil
}

func (c *Consumer) Start(ctx context.Context) error {
	logger := zerolog.Ctx(ctx).With().Str("consumer", c.name).Str("topic", c.topic).Logger()
	logger.Info().Msg("kafka consumer: subscribing")
	messages, err := c.subscriber.Subscribe(ctx, c.topic)
	if err != nil {
		logger.Error().Err(err).Msg("kafka consumer: subscribe failed")
		return fmt.Errorf("could not subscribe to topic %q: %w", c.topic, err)
	}
	logger.Info().Msg("kafka consumer: subscribed, entering processor")
	if c.Processor == nil {
		return fmt.Errorf("processor function is nil")
	}

	err = c.Processor(ctx, messages, c.maxInFlight)
	logger.Info().Err(err).Msg("kafka consumer: processor returned")
	return err
}

func (c *Consumer) Stop(ctx context.Context) error {
	return c.subscriber.Close()
}

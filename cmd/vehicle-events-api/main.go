package main

import (
	"context"
	"log"
	"os"
	"strings"

	"github.com/rs/zerolog"

	"github.com/DIMO-Network/shared"
	sharedDB "github.com/DIMO-Network/shared/db"
	"github.com/DIMO-Network/vehicle-events-api/internal/api"
	"github.com/DIMO-Network/vehicle-events-api/internal/config"
	"github.com/DIMO-Network/vehicle-events-api/internal/db"
	"github.com/DIMO-Network/vehicle-events-api/internal/kafka"
	"github.com/DIMO-Network/vehicle-events-api/internal/services"

	"github.com/IBM/sarama"
)

func main() {
	gitSha1 := os.Getenv("GIT_SHA1")
	ctx := context.Background()

	settings, err := shared.LoadConfig[config.Settings]("settings.yaml")
	if err != nil {
		log.Fatalf("could not load settings: %s", err)
	}

	level, err := zerolog.ParseLevel(settings.LogLevel)
	if err != nil {
		log.Fatalf("could not parse log level: %s", err)
	}
	logger := zerolog.New(os.Stdout).Level(level).With().
		Timestamp().
		Str("app", settings.ServiceName).
		Str("git-sha1", gitSha1).
		Logger()

	args := os.Args
	if len(args) > 1 && strings.ToLower(args[1]) == "migrate" {
		db.MigrateDatabase(ctx, logger, &settings, args)
		return
	}

	store := sharedDB.NewDbConnectionFromSettings(ctx, &settings.DB, true)
	store.WaitForDB(logger)

	startDeviceSignalsConsumer(ctx, logger, &settings)

	api.Run(ctx, logger, store)
}

// startDeviceSignalsConsumer sets up and starts the Kafka consumer for topic.device.signals
func startDeviceSignalsConsumer(ctx context.Context, logger zerolog.Logger, settings *config.Settings) {
	clusterConfig := sarama.NewConfig()
	clusterConfig.Version = sarama.V2_8_1_0
	clusterConfig.Consumer.Offsets.Initial = sarama.OffsetOldest

	consumerConfig := &kafka.Config{
		ClusterConfig:   clusterConfig,
		BrokerAddresses: strings.Split(settings.KafkaBrokers, ","),
		Topic:           settings.DeviceSignalsTopic,
		GroupID:         "vehicle-events",
		MaxInFlight:     1,
	}

	consumer, err := kafka.NewConsumer(consumerConfig, &logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("Could not create device signals consumer")
	}

	// Initialize the in-memory webhook cache.
	webhookCache := services.NewWebhookCache()
	signalListener := services.NewSignalListener(logger, webhookCache)
	consumer.Start(ctx, signalListener.ProcessSignals)

	logger.Info().Msgf("Device signals consumer started on topic: %s", settings.DeviceSignalsTopic)
}

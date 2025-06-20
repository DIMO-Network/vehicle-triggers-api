package main

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"github.com/DIMO-Network/shared"
	sharedDB "github.com/DIMO-Network/shared/db"
	"github.com/DIMO-Network/vehicle-events-api/internal/api"
	"github.com/DIMO-Network/vehicle-events-api/internal/config"
	"github.com/DIMO-Network/vehicle-events-api/internal/db"
	"github.com/DIMO-Network/vehicle-events-api/internal/gateways"
	"github.com/DIMO-Network/vehicle-events-api/internal/kafka"
	"github.com/DIMO-Network/vehicle-events-api/internal/services"
	"github.com/IBM/sarama"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
)

// @title           Vehicle Events API
// @version         1.0
//
// @securityDefinitions.apikey BearerAuth
// @in                         header
// @name                       Authorization
// @description                JWT Authorization header using the Bearer scheme. Example: "Bearer {token}"
//
// @BasePath  /
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

	logger.Info().
		Msgf("Connecting to Postgres as %s@%s", settings.DB.User, settings.DB.Name)

	args := os.Args
	if len(args) > 1 && strings.ToLower(args[1]) == "migrate" {
		db.MigrateDatabase(ctx, logger, &settings, args)
		return
	}

	store := sharedDB.NewDbConnectionFromSettings(ctx, &settings.DB, true)
	store.WaitForDB(logger)

	logger.Info().
		Str("db_host", settings.DB.Host).
		Str("db_name", settings.DB.Name).
		Msg("Connected to database")

	monApp := createMonitoringServer()
	go func() {
		monPort := settings.MonitoringPort
		if err := monApp.Listen(":" + monPort); err != nil {
			logger.Error().Err(err).Msg("Monitoring server failed")
		}
	}()

	identityAPI := gateways.NewIdentityAPIService(settings.IdentityAPIURL, logger)
	webhookCache := startDeviceSignalsConsumer(ctx, logger, &settings, identityAPI)
	api.Run(logger, store, webhookCache, identityAPI, &settings)
}

// startDeviceSignalsConsumer sets up and starts the Kafka consumer for topic.device.signals
func startDeviceSignalsConsumer(ctx context.Context, logger zerolog.Logger, settings *config.Settings, identityAPI gateways.IdentityAPI) *services.WebhookCache {
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
	webhookCache := services.NewWebhookCache(&logger)

	store := sharedDB.NewDbConnectionFromSettings(ctx, &settings.DB, true)
	store.WaitForDB(logger)

	//load all existing webhooks into memory** so GetWebhooks() won't be empty
	if err := webhookCache.PopulateCache(ctx, store.DBS().Reader); err != nil {
		logger.Fatal().Err(err).Msg("Unable to populate webhook cache at startup")
	}

	// Periodically refresh the cache so new/updated webhooks show up without a restart
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			if err := webhookCache.PopulateCache(ctx, store.DBS().Reader); err != nil {
				logger.Error().Err(err).Msg("Periodic cache refresh failed")
			}
		}
	}()

	signalListener := services.NewSignalListener(logger, webhookCache, store, identityAPI)
	consumer.Start(ctx, signalListener.ProcessSignals)

	logger.Info().Msgf("Device signals consumer started on topic: %s", settings.DeviceSignalsTopic)

	return webhookCache
}

func createMonitoringServer() *fiber.App {
	monApp := fiber.New(fiber.Config{DisableStartupMessage: true})

	monApp.Get("/", func(*fiber.Ctx) error { return nil })
	monApp.Get("/metrics", adaptor.HTTPHandler(promhttp.Handler()))

	return monApp
}

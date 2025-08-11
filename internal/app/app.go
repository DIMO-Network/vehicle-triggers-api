package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/DIMO-Network/server-garage/pkg/fibercommon"
	"github.com/DIMO-Network/shared/pkg/db"
	_ "github.com/DIMO-Network/vehicle-triggers-api/docs" // Import Swagger docs
	"github.com/DIMO-Network/vehicle-triggers-api/internal/clients/identity"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/clients/tokenexchange"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/config"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/vehiclelistener"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/webhook"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/kafka"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhookcache"
	"github.com/IBM/sarama"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/swagger"
	"github.com/rs/zerolog"
)

func CreateServers(ctx context.Context, settings *config.Settings, logger zerolog.Logger) (*fiber.App, error) {
	store := db.NewDbConnectionFromSettings(ctx, &settings.DB, true)
	store.WaitForDB(logger)

	tokenExchangeAPI, err := tokenexchange.New(settings)
	if err != nil {
		return nil, fmt.Errorf("failed to create token exchange API: %w", err)
	}

	repo := triggersrepo.NewRepository(store.DBS().Writer.DB)

	webhookCache, err := startDeviceSignalsConsumer(ctx, logger, settings, tokenExchangeAPI, repo)
	if err != nil {
		return nil, fmt.Errorf("failed to start device signals consumer: %w", err)
	}

	identityClient, err := identity.New(settings, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create identity client: %w", err)
	}

	app, err := CreateFiberApp(logger, repo, webhookCache, tokenExchangeAPI, identityClient, settings)
	if err != nil {
		return nil, fmt.Errorf("failed to create fiber app: %w", err)
	}
	return app, nil
}

// Run sets up the API routes and starts the HTTP server.
func CreateFiberApp(logger zerolog.Logger, repo *triggersrepo.Repository,
	webhookCache *webhookcache.WebhookCache,
	tokenExchangeClient *tokenexchange.Client,
	identityClient *identity.Client,
	settings *config.Settings) (*fiber.App, error) {
	logger.Info().Msg("Starting Vehicle Triggers API...")

	app := fiber.New(fiber.Config{
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			return fibercommon.ErrorHandler(c, err)
		},
		DisableStartupMessage: true,
	})
	app.Use(fibercommon.ContextLoggerMiddleware)

	app.Get("/swagger/*", swagger.HandlerDefault)

	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendString("Welcome to the Vehicle Triggers API!")
	})

	// Create a JWT middleware that verifies developer licenses.
	// settings.IdentityAPIURL is loaded from your settings.yaml.
	jwtMiddleware := webhook.JWTMiddleware(identityClient, logger)
	// Register Webhook routes.
	webhookController, err := webhook.NewWebhookController(repo, webhookCache)
	if err != nil {
		return nil, fmt.Errorf("failed to create webhook controller: %w", err)
	}
	vehicleSubscriptionController := webhook.NewVehicleSubscriptionController(repo, identityClient, tokenExchangeClient, webhookCache)
	logger.Info().Msg("Registering routes...")

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"data": "Server is up and running",
		})
	})

	// Webhook CRUD
	app.Get("/v1/webhooks", jwtMiddleware, webhookController.ListWebhooks)
	app.Post("/v1/webhooks", jwtMiddleware, webhookController.RegisterWebhook)
	app.Get("/v1/webhooks/signals", jwtMiddleware, webhookController.GetSignalNames)
	app.Get("/v1/webhooks/:webhookId", jwtMiddleware, vehicleSubscriptionController.ListVehiclesForWebhook)
	app.Put("/v1/webhooks/:webhookId", jwtMiddleware, webhookController.UpdateWebhook)
	app.Delete("/v1/webhooks/:webhookId", jwtMiddleware, webhookController.DeleteWebhook)

	// Vehicle subscriptions
	app.Post("/v1/webhooks/:webhookId/subscribe/list", jwtMiddleware, vehicleSubscriptionController.SubscribeVehiclesFromList)
	app.Post("/v1/webhooks/:webhookId/subscribe/all", jwtMiddleware, vehicleSubscriptionController.SubscribeAllVehiclesToWebhook)
	app.Post("/v1/webhooks/:webhookId/subscribe/:vehicleTokenId", jwtMiddleware, vehicleSubscriptionController.AssignVehicleToWebhook)
	app.Delete("/v1/webhooks/:webhookId/unsubscribe/list", jwtMiddleware, vehicleSubscriptionController.UnsubscribeVehiclesFromList)
	app.Delete("/v1/webhooks/:webhookId/unsubscribe/all", jwtMiddleware, vehicleSubscriptionController.UnsubscribeAllVehiclesFromWebhook)
	app.Delete("/v1/webhooks/:webhookId/unsubscribe/:vehicleTokenId", jwtMiddleware, vehicleSubscriptionController.RemoveVehicleFromWebhook)
	app.Get("/v1/webhooks/vehicles/:vehicleTokenId", jwtMiddleware, vehicleSubscriptionController.ListSubscriptions)

	return app, nil
}

// startDeviceSignalsConsumer sets up and starts the Kafka consumer for topic.device.signals
func startDeviceSignalsConsumer(ctx context.Context, logger zerolog.Logger, settings *config.Settings, tokenExchangeAPI *tokenexchange.Client, repo *triggersrepo.Repository) (*webhookcache.WebhookCache, error) {
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

	consumer, err := kafka.NewConsumer(consumerConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create device signals consumer: %w", err)
	}

	// Initialize the in-memory webhook cache.
	webhookCache := webhookcache.NewWebhookCache(repo)

	//load all existing webhooks into memory** so GetWebhooks() won't be empty
	if err := webhookCache.PopulateCache(ctx); err != nil {
		return nil, fmt.Errorf("failed to populate webhook cache at startup: %w", err)
	}

	// Periodically refresh the cache so new/updated webhooks show up without a restart
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			if err := webhookCache.PopulateCache(ctx); err != nil {
				logger.Error().Err(err).Msg("Periodic cache refresh failed")
			}
		}
	}()

	signalListener := vehiclelistener.NewSignalListener(webhookCache, repo, tokenExchangeAPI)
	if err := consumer.Start(ctx, signalListener.ProcessSignals); err != nil {
		return nil, fmt.Errorf("failed to start device signals consumer: %w", err)
	}

	logger.Info().Msgf("Device signals consumer started on topic: %s", settings.DeviceSignalsTopic)

	return webhookCache, nil
}

package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/DIMO-Network/server-garage/pkg/fibercommon"
	"github.com/DIMO-Network/shared/pkg/db"
	_ "github.com/DIMO-Network/vehicle-triggers-api/docs" // Import Swagger docs
	"github.com/DIMO-Network/vehicle-triggers-api/internal/auth"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/clients/identity"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/clients/tokenexchange"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/config"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/metriclistener"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/webhook"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/kafka"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhookcache"
	"github.com/IBM/sarama"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/swagger"
	"github.com/rs/zerolog"
)

type Servers struct {
	Application    *fiber.App
	SignalConsumer *kafka.Consumer
	EventConsumer  *kafka.Consumer
}

func CreateServers(ctx context.Context, settings *config.Settings, logger zerolog.Logger) (*Servers, error) {
	store := db.NewDbConnectionFromSettings(ctx, &settings.DB, true)
	store.WaitForDB(logger)

	tokenExchangeAPI, err := tokenexchange.New(settings)
	if err != nil {
		return nil, fmt.Errorf("failed to create token exchange API: %w", err)
	}

	repo := triggersrepo.NewRepository(store.DBS().Writer.DB)

	webhookCache, err := startWebhookCache(ctx, settings, tokenExchangeAPI, repo)
	if err != nil {
		return nil, fmt.Errorf("failed to start webhook cache: %w", err)
	}

	signalConsumer, err := createSignalConsumer(ctx, settings, tokenExchangeAPI, repo, webhookCache)
	if err != nil {
		return nil, fmt.Errorf("failed to create signal consumer: %w", err)
	}

	eventConsumer, err := createEventConsumer(ctx, settings, tokenExchangeAPI, repo, webhookCache)
	if err != nil {
		return nil, fmt.Errorf("failed to create event consumer: %w", err)
	}

	identityClient, err := identity.New(settings, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create identity client: %w", err)
	}

	app, err := CreateFiberApp(logger, repo, webhookCache, tokenExchangeAPI, identityClient, settings)
	if err != nil {
		return nil, fmt.Errorf("failed to create fiber app: %w", err)
	}
	return &Servers{
		Application:    app,
		SignalConsumer: signalConsumer,
		EventConsumer:  eventConsumer,
	}, nil
}

// Run sets up the API routes and starts the HTTP server.
func CreateFiberApp(logger zerolog.Logger, repo *triggersrepo.Repository,
	webhookCache *webhookcache.WebhookCache,
	tokenExchangeClient *tokenexchange.Client,
	identityClient *identity.Client,
	settings *config.Settings) (*fiber.App, error) {

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

	// Register Webhook routes.
	webhookController, err := webhook.NewWebhookController(repo, webhookCache)
	if err != nil {
		return nil, fmt.Errorf("failed to create webhook controller: %w", err)
	}
	vehicleSubscriptionController := webhook.NewVehicleSubscriptionController(repo, identityClient, tokenExchangeClient, webhookCache)

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"data": "Server is up and running",
		})
	})

	jwtMiddleware := auth.Middleware(settings)
	devLicenseMiddleware := auth.NewDevLicenseValidator(identityClient)
	devJWTAuth := app.Use(jwtMiddleware, devLicenseMiddleware)
	// Webhook CRUD
	devJWTAuth.Get("/v1/webhooks", webhookController.ListWebhooks)
	devJWTAuth.Post("/v1/webhooks", webhookController.RegisterWebhook)
	devJWTAuth.Get("/v1/webhooks/signals", webhookController.GetSignalNames)
	devJWTAuth.Get("/v1/webhooks/:webhookId", vehicleSubscriptionController.ListVehiclesForWebhook)
	devJWTAuth.Put("/v1/webhooks/:webhookId", webhookController.UpdateWebhook)
	devJWTAuth.Delete("/v1/webhooks/:webhookId", webhookController.DeleteWebhook)

	// Vehicle subscriptions
	devJWTAuth.Post("/v1/webhooks/:webhookId/subscribe/list", vehicleSubscriptionController.SubscribeVehiclesFromList)
	devJWTAuth.Post("/v1/webhooks/:webhookId/subscribe/all", vehicleSubscriptionController.SubscribeAllVehiclesToWebhook)
	devJWTAuth.Post("/v1/webhooks/:webhookId/subscribe/:assetDID", vehicleSubscriptionController.AssignVehicleToWebhook)
	devJWTAuth.Delete("/v1/webhooks/:webhookId/unsubscribe/list", vehicleSubscriptionController.UnsubscribeVehiclesFromList)
	devJWTAuth.Delete("/v1/webhooks/:webhookId/unsubscribe/all", vehicleSubscriptionController.UnsubscribeAllVehiclesFromWebhook)
	devJWTAuth.Delete("/v1/webhooks/:webhookId/unsubscribe/:assetDID", vehicleSubscriptionController.RemoveVehicleFromWebhook)
	devJWTAuth.Get("/v1/webhooks/vehicles/:assetDID", vehicleSubscriptionController.ListSubscriptions)

	return app, nil
}

// startWebhookCache sets up and starts the Kafka consumer for topic.device.signals
func startWebhookCache(ctx context.Context, settings *config.Settings, tokenExchangeAPI *tokenexchange.Client, repo *triggersrepo.Repository) (*webhookcache.WebhookCache, error) {
	// Initialize the in-memory webhook cache.
	webhookCache := webhookcache.NewWebhookCache(repo, settings)

	//load all existing webhooks into memory** so GetWebhooks() won't be empty
	if err := webhookCache.PopulateCache(ctx); err != nil {
		return nil, fmt.Errorf("failed to populate webhook cache at startup: %w", err)
	}

	logger := zerolog.Ctx(ctx)
	// Periodically refresh the cache so new/updated webhooks show up without a restart
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			webhookCache.ScheduleRefresh(ctx)
		}
	}()

	logger.Info().Msgf("Device signals consumer started on topic: %s", settings.DeviceSignalsTopic)

	return webhookCache, nil
}

func createSignalConsumer(ctx context.Context, settings *config.Settings, tokenExchangeAPI *tokenexchange.Client, repo *triggersrepo.Repository, webhookCache *webhookcache.WebhookCache) (*kafka.Consumer, error) {
	clusterConfig := sarama.NewConfig()
	clusterConfig.Version = sarama.V2_8_1_0
	clusterConfig.Consumer.Offsets.Initial = sarama.OffsetOldest
	vehicleProcessor := metriclistener.NewMetricsListener(webhookCache, repo, tokenExchangeAPI, settings)
	consumerConfig := &kafka.Config{
		ClusterConfig:   clusterConfig,
		BrokerAddresses: strings.Split(settings.KafkaBrokers, ","),
		Topic:           settings.DeviceSignalsTopic,
		GroupID:         "vehicle-triggers",
		MaxInFlight:     1,
		Processor:       vehicleProcessor.ProcessSignalMessages,
	}

	consumer, err := kafka.NewConsumer(consumerConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create device signals consumer: %w", err)
	}

	return consumer, nil
}

func createEventConsumer(ctx context.Context, settings *config.Settings, tokenExchangeAPI *tokenexchange.Client, repo *triggersrepo.Repository, webhookCache *webhookcache.WebhookCache) (*kafka.Consumer, error) {
	clusterConfig := sarama.NewConfig()
	clusterConfig.Version = sarama.V2_8_1_0
	clusterConfig.Consumer.Offsets.Initial = sarama.OffsetOldest
	vehicleProcessor := metriclistener.NewMetricsListener(webhookCache, repo, tokenExchangeAPI, settings)
	consumerConfig := &kafka.Config{
		ClusterConfig:   clusterConfig,
		BrokerAddresses: strings.Split(settings.KafkaBrokers, ","),
		Topic:           settings.DeviceEventsTopic,
		GroupID:         "vehicle-triggers",
		MaxInFlight:     1,
		Processor:       vehicleProcessor.ProcessEventMessages,
	}

	consumer, err := kafka.NewConsumer(consumerConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create device events consumer: %w", err)
	}

	return consumer, nil
}

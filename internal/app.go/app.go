package app

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/DIMO-Network/shared/pkg/db"
	_ "github.com/DIMO-Network/vehicle-triggers-api/docs" // Import Swagger docs
	"github.com/DIMO-Network/vehicle-triggers-api/internal/config"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/gateways"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/kafka"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services"
	"github.com/IBM/sarama"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/rs/zerolog"
	fiberSwagger "github.com/swaggo/fiber-swagger"
)

func CreateServers(ctx context.Context, settings *config.Settings, logger zerolog.Logger) (*fiber.App, error) {
	store := db.NewDbConnectionFromSettings(ctx, &settings.DB, true)
	store.WaitForDB(logger)
	identityAPI := gateways.NewIdentityAPIService(settings.IdentityAPIURL, logger)

	webhookCache := startDeviceSignalsConsumer(ctx, logger, settings, identityAPI, store)

	app := CreateFiberApp(logger, store, webhookCache, identityAPI, settings)
	return app, nil
}

// Run sets up the API routes and starts the HTTP server.
func CreateFiberApp(logger zerolog.Logger, store db.Store, webhookCache *services.WebhookCache, identityAPI gateways.IdentityAPI, settings *config.Settings) *fiber.App {
	logger.Info().Msg("Starting Vehicle Triggers API...")

	app := fiber.New()

	app.Use(cors.New(cors.Config{
		AllowOrigins: "*",
		AllowMethods: "GET,POST,PUT,DELETE,OPTIONS",
		AllowHeaders: "*",
	}))

	app.Get("/swagger/*", fiberSwagger.WrapHandler)

	app.Get("/doc.json", func(c *fiber.Ctx) error {
		path, _ := filepath.Abs("./docs/swagger.json")
		return c.SendFile(path)
	})

	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendString("Welcome to the Vehicle Events API!")
	})

	// Create a JWT middleware that verifies developer licenses.
	// settings.IdentityAPIURL is loaded from your settings.yaml.
	jwtMiddleware := controllers.JWTMiddleware(store, identityAPI, logger)
	// Register Webhook routes.
	webhookController := controllers.NewWebhookController(store, logger)
	vehicleSubscriptionController := controllers.NewVehicleSubscriptionController(store, logger, identityAPI, webhookCache)
	logger.Info().Msg("Registering routes...")

	app.Post("/build-cel", webhookController.BuildCEL)

	// Webhook CRUD
	app.Get("/v1/webhooks", jwtMiddleware, webhookController.ListWebhooks)
	app.Post("/v1/webhooks", jwtMiddleware, webhookController.RegisterWebhook)
	app.Get("/v1/webhooks/signals", jwtMiddleware, webhookController.GetSignalNames)
	app.Get("/v1/webhooks/:webhookId", jwtMiddleware, vehicleSubscriptionController.ListVehiclesForWebhook)
	app.Put("/v1/webhooks/:webhookId", jwtMiddleware, webhookController.UpdateWebhook)
	app.Delete("/v1/webhooks/:webhookId", jwtMiddleware, webhookController.DeleteWebhook)

	// Vehicle subscriptions
	app.Post("/v1/webhooks/:webhookId/subscribe/csv", jwtMiddleware, vehicleSubscriptionController.SubscribeVehiclesFromCSV)
	app.Post("/v1/webhooks/:webhookId/subscribe/all", jwtMiddleware, vehicleSubscriptionController.SubscribeAllVehiclesToWebhook)
	app.Post("/v1/webhooks/:webhookId/subscribe/:vehicleTokenId", jwtMiddleware, vehicleSubscriptionController.AssignVehicleToWebhook)
	app.Delete("/v1/webhooks/:webhookId/unsubscribe/csv", jwtMiddleware, vehicleSubscriptionController.UnsubscribeVehiclesFromCSV)
	app.Delete("/v1/webhooks/:webhookId/unsubscribe/all", jwtMiddleware, vehicleSubscriptionController.UnsubscribeAllVehiclesFromWebhook)
	app.Delete("/v1/webhooks/:webhookId/unsubscribe/:vehicleTokenId", jwtMiddleware, vehicleSubscriptionController.RemoveVehicleFromWebhook)
	app.Get("/v1/webhooks/vehicles/:vehicleTokenId", jwtMiddleware, vehicleSubscriptionController.ListSubscriptions)

	// Catchall route.
	app.Use(func(c *fiber.Ctx) error {
		logger.Warn().
			Str("method", c.Method()).
			Str("path", c.Path()).
			Msg("404 Not Found")
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Not Found"})
	})

	return app
}

// startDeviceSignalsConsumer sets up and starts the Kafka consumer for topic.device.signals
func startDeviceSignalsConsumer(ctx context.Context, logger zerolog.Logger, settings *config.Settings, identityAPI gateways.IdentityAPI, store db.Store) *services.WebhookCache {
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

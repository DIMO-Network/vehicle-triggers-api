package api

import (
	"context"
	"github.com/DIMO-Network/shared"
	"github.com/DIMO-Network/shared/db"
	_ "github.com/DIMO-Network/vehicle-events-api/docs" // Import Swagger docs
	"github.com/DIMO-Network/vehicle-events-api/internal/config"
	"github.com/DIMO-Network/vehicle-events-api/internal/controllers"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/rs/zerolog"
	fiberSwagger "github.com/swaggo/fiber-swagger"
	"path/filepath"
)

func healthCheck(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"code":    200,
		"message": "server is up",
	})
}

// Run sets up the API routes and starts the HTTP server.
func Run(ctx context.Context, logger zerolog.Logger, store db.Store) {
	logger.Info().Msg("Starting Vehicle Events API...")

	settings, err := shared.LoadConfig[config.Settings]("settings.yaml")
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to load settings")
	}
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
	jwtMiddleware := controllers.JWTMiddleware(store, settings.IdentityAPIURL, logger)

	// Register Webhook routes.
	webhookController := controllers.NewWebhookController(store, logger)

	logger.Info().Msg("Registering routes...")
	app.Post("/webhooks", jwtMiddleware, webhookController.RegisterWebhook)
	app.Get("/webhooks", jwtMiddleware, webhookController.ListWebhooks)
	app.Put("/webhooks/:id", jwtMiddleware, webhookController.UpdateWebhook)
	app.Delete("/webhooks/:id", jwtMiddleware, webhookController.DeleteWebhook)
	app.Get("/webhooks/signals", jwtMiddleware, webhookController.GetSignalNames)

	// Endpoint to build a CEL expression from user-defined conditions.
	app.Post("/build-cel", webhookController.BuildCEL)

	// Register Vehicle Subscription routes.
	vehicleSubscriptionController := controllers.NewVehicleSubscriptionController(store, logger)
	app.Post("/subscriptions/:vehicleTokenID/event/:eventID", jwtMiddleware, vehicleSubscriptionController.AssignVehicleToWebhook)
	app.Delete("/subscriptions/:vehicleTokenID/event/:eventID", jwtMiddleware, vehicleSubscriptionController.RemoveVehicleFromWebhook)
	app.Get("/subscriptions/:vehicleTokenID", jwtMiddleware, vehicleSubscriptionController.ListSubscriptions)
	app.Post("/subscriptions/all/event/:eventID", vehicleSubscriptionController.SubscribeAllVehiclesToWebhook)
	app.Post("/subscriptions/:eventID", vehicleSubscriptionController.SubscribeMultipleVehiclesToWebhook)

	app.Get("/health", healthCheck)

	// Catchall route.
	app.Use(func(c *fiber.Ctx) error {
		logger.Warn().
			Str("method", c.Method()).
			Str("path", c.Path()).
			Msg("404 Not Found")
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Not Found"})
	})

	// Start the server.
	logger.Info().Msgf("Starting HTTP server on :%s...", settings.Port)
	if err := app.Listen(":" + settings.Port); err != nil {
		logger.Fatal().Err(err).Msg("Server failed to start")
	}
}

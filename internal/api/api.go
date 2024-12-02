package api

import (
	"context"

	"github.com/DIMO-Network/shared/db"
	"github.com/DIMO-Network/vehicle-events-api/internal/config"
	"github.com/DIMO-Network/vehicle-events-api/internal/controllers"
	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog"
	fiberSwagger "github.com/swaggo/fiber-swagger"
)

// Run sets up the API routes and starts the HTTP server.
func Run(ctx context.Context, logger zerolog.Logger, settings *config.Settings, store db.Store) {
	app := fiber.New()

	// Serve Swagger UI
	app.Get("/swagger/*", fiberSwagger.WrapHandler)

	// Serve Swagger JSON
	app.Get("/swagger.json", func(c *fiber.Ctx) error {
		return c.SendFile("./docs/swagger.json")
	})

	// Optional root route
	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendString("Welcome to the Vehicle Events API!")
	})

	// Initialize WebhookController with database store and logger
	webhookController := controllers.NewWebhookController(store, logger)

	// Webhook CRUD routes
	app.Post("/webhooks", webhookController.RegisterWebhook)
	app.Get("/webhooks", webhookController.ListWebhooks)
	app.Put("/webhooks/:id", webhookController.UpdateWebhook)
	app.Delete("/webhooks/:id", webhookController.DeleteWebhook)

	// Start the server
	if err := app.Listen(":8080"); err != nil {
		logger.Fatal().Err(err).Msg("Server failed to start")
	}
}

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

	app.Get("/swagger/*", fiberSwagger.WrapHandler)

	app.Get("/swagger.json", func(c *fiber.Ctx) error {
		return c.SendFile("./docs/swagger.json")
	})

	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendString("Welcome to the Vehicle Events API!")
	})

	webhookController := controllers.NewWebhookController(store, logger)

	app.Post("/webhooks", webhookController.RegisterWebhook)
	app.Get("/webhooks", webhookController.ListWebhooks)
	app.Put("/webhooks/:id", webhookController.UpdateWebhook)
	app.Delete("/webhooks/:id", webhookController.DeleteWebhook)

	if err := app.Listen(":8080"); err != nil {
		logger.Fatal().Err(err).Msg("Server failed to start")
	}
}

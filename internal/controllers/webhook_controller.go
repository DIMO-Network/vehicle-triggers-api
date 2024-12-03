package controllers

import (
	"encoding/json"
	"github.com/DIMO-Network/shared/db"
	"github.com/DIMO-Network/vehicle-events-api/internal/infrastructure/db/models"
	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog"
	"github.com/teris-io/shortid"
	"github.com/volatiletech/null/v8"
	"github.com/volatiletech/sqlboiler/v4/boil"
	"github.com/volatiletech/sqlboiler/v4/queries/qm"
)

type WebhookController struct {
	store  db.Store
	logger zerolog.Logger
}

func generateShortID() string {
	id, err := shortid.Generate()
	if err != nil {
		panic("Failed to generate short ID")
	}
	return id
}

func NewWebhookController(store db.Store, logger zerolog.Logger) *WebhookController {
	return &WebhookController{
		store:  store,
		logger: logger,
	}
}

// RegisterWebhook godoc
// @Summary      Register a new webhook
// @Description  Registers a new webhook with the specified configuration.
// @Tags         Webhooks
// @Accept       json
// @Produce      json
// @Param        request  body      object  true  "Request payload"
// @Success      201      "Webhook registered successfully"
// @Failure      400      "Invalid request payload"
// @Failure      500      "Internal server error"
// @Router       /webhooks [post]
func (w *WebhookController) RegisterWebhook(c *fiber.Ctx) error {
	type RequestPayload struct {
		Service     string                 `json:"service" validate:"required"`
		Data        string                 `json:"data" validate:"required"`
		Trigger     string                 `json:"trigger" validate:"required"`
		Setup       string                 `json:"setup" validate:"required"`
		Description string                 `json:"description"`
		TargetURI   string                 `json:"target_uri" validate:"required"`
		Parameters  map[string]interface{} `json:"parameters"`
	}

	var payload RequestPayload
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request payload"})
	}

	parametersJSON, err := json.Marshal(payload.Parameters)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Failed to serialize parameters"})
	}

	event := &models.Event{
		ID:                      generateShortID(),
		Service:                 payload.Service,
		Data:                    payload.Data,
		Trigger:                 payload.Trigger,
		Setup:                   payload.Setup,
		TargetURI:               payload.TargetURI,
		Parameters:              null.JSONFrom(parametersJSON),
		DeveloperLicenseAddress: []byte{0x12, 0x34, 0x56, 0x78, 0x90, 0xab, 0xcd, 0xef}, // Hex-decoded license address
		Status:                  "Active",
	}

	if err := event.Insert(c.Context(), w.store.DBS().Writer, boil.Infer()); err != nil {
		w.logger.Error().Err(err).Msg("Failed to insert webhook into database")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to register webhook"})
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"id": event.ID, "message": "Webhook registered successfully"})
}

// ListWebhooks godoc
// @Summary      List all webhooks
// @Description  Retrieves all registered webhooks for the developer.
// @Tags         Webhooks
// @Produce      json
// @Success      200  {array}  object  "List of webhooks"
// @Failure      500  "Internal server error"
// @Router       /webhooks [get]
func (w *WebhookController) ListWebhooks(c *fiber.Ctx) error {
	events, err := models.Events(qm.OrderBy("id")).All(c.Context(), w.store.DBS().Reader)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to retrieve webhooks"})
	}
	return c.JSON(events)
}

// UpdateWebhook godoc
// @Summary      Update a webhook
// @Description  Updates the configuration of a webhook by its ID.
// @Tags         Webhooks
// @Accept       json
// @Produce      json
// @Param        id       path      string  true  "Webhook ID"
// @Param        request  body      object  true  "Request payload"
// @Success      200      "Webhook updated successfully"
// @Failure      400      "Invalid request payload"
// @Failure      404      "Webhook not found"
// @Failure      500      "Internal server error"
// @Router       /webhooks/{id} [put]
func (w *WebhookController) UpdateWebhook(c *fiber.Ctx) error {
	id := c.Params("id")

	event, err := models.FindEvent(c.Context(), w.store.DBS().Reader, id)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Webhook not found"})
	}

	var updates map[string]interface{}
	if err := c.BodyParser(&updates); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request payload"})
	}

	if service, ok := updates["service"].(string); ok {
		event.Service = service
	}
	if parameters, ok := updates["parameters"].(map[string]interface{}); ok {
		parametersJSON, err := json.Marshal(parameters)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Failed to serialize parameters"})
		}
		event.Parameters = null.JSONFrom(parametersJSON) // Use null.JSONFrom
	}

	rowsAffected, err := event.Update(c.Context(), w.store.DBS().Writer, boil.Infer())
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to update webhook"})
	}

	if rowsAffected == 0 {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "No webhook was updated"})
	}

	return c.JSON(fiber.Map{"id": id, "message": "Webhook updated successfully"})
}

// DeleteWebhook godoc
// @Summary      Delete a webhook
// @Description  Deletes a webhook by its ID.
// @Tags         Webhooks
// @Produce      json
// @Param        id  path  string  true  "Webhook ID"
// @Success      204  "Webhook deleted successfully"
// @Failure      404  "Webhook not found"
// @Failure      500  "Internal server error"
// @Router       /webhooks/{id} [delete]
func (w *WebhookController) DeleteWebhook(c *fiber.Ctx) error {
	id := c.Params("id")

	event, err := models.FindEvent(c.Context(), w.store.DBS().Reader, id)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Webhook not found"})
	}

	if _, err := event.Delete(c.Context(), w.store.DBS().Writer); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to delete webhook"})
	}

	return c.JSON(fiber.Map{"id": id, "message": "Webhook deleted successfully"})
}

package controllers

import (
	"database/sql"
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

func generateShortID(logger zerolog.Logger) string {
	id, err := shortid.Generate()
	if err != nil {
		logger.Error().Err(err).Msg("Failed to generate short ID")
		return ""
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
// @Failure      401  "Unauthorized"
// @Failure      500  "Internal server error"
// @Router       /webhooks [get]
func (w *WebhookController) ListWebhooks(c *fiber.Ctx) error {
	// Extract developer license address from the request context
	devLicense, ok := c.Locals("developer_license_address").([]byte)
	if !ok {
		w.logger.Error().Msg("Developer license not found in request context")
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	// Query webhooks associated with the developer license
	events, err := models.Events(
		qm.Where("developer_license_address = ?", devLicense),
		qm.OrderBy("id"),
	).All(c.Context(), w.store.DBS().Reader)
	if err != nil {
		w.logger.Error().Err(err).Msg("Failed to retrieve webhooks")
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
	devLicense, ok := c.Locals("developer_license_address").([]byte)
	if !ok {
		w.logger.Error().Msg("Developer license not found in request context")
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	// Retrieve the webhook and make sure it belongs to the developer
	event, err := models.Events(
		qm.Where("id = ? AND developer_license_address = ?", id, devLicense),
	).One(c.Context(), w.store.DBS().Reader)
	if err != nil {
		if err == sql.ErrNoRows {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Webhook not found"})
		}
		w.logger.Error().Err(err).Msg("Failed to retrieve webhook")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to retrieve webhook"})
	}

	// Parse the update payload
	type RequestPayload struct {
		Setup      string                 `json:"setup"`
		TargetURI  string                 `json:"target_uri"`
		Parameters map[string]interface{} `json:"parameters"`
	}

	var payload RequestPayload
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request payload"})
	}

	// Update the webhook
	if payload.Setup != "" {
		event.Setup = payload.Setup
	}
	if payload.TargetURI != "" {
		event.TargetURI = payload.TargetURI
	}
	if payload.Parameters != nil {
		parametersJSON, err := json.Marshal(payload.Parameters)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Failed to serialize parameters"})
		}
		event.Parameters = null.JSONFrom(parametersJSON)

	}

	if _, err := event.Update(c.Context(), w.store.DBS().Writer, boil.Infer()); err != nil {
		w.logger.Error().Err(err).Msg("Failed to update webhook")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to update webhook"})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{"message": "Webhook updated successfully"})
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
	devLicense, ok := c.Locals("developer_license_address").([]byte)
	if !ok {
		w.logger.Error().Msg("Developer license not found in request context")
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	//Make sure the webhook belongs to the developer
	event, err := models.Events(
		qm.Where("id = ? AND developer_license_address = ?", id, devLicense),
	).One(c.Context(), w.store.DBS().Reader)
	if err != nil {
		if err == sql.ErrNoRows {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Webhook not found"})
		}
		w.logger.Error().Err(err).Msg("Failed to retrieve webhook")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to retrieve webhook"})
	}

	// Delete the webhook
	if _, err := event.Delete(c.Context(), w.store.DBS().Writer); err != nil {
		w.logger.Error().Err(err).Msg("Failed to delete webhook")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to delete webhook"})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{"message": "Webhook deleted successfully"})
}

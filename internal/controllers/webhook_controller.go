package controllers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/DIMO-Network/model-garage/pkg/schema"
	"github.com/DIMO-Network/shared/db"
	"github.com/DIMO-Network/vehicle-events-api/internal/infrastructure/db/models"
	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog"
	"github.com/teris-io/shortid"
	"github.com/volatiletech/null/v8"
	"github.com/volatiletech/sqlboiler/v4/boil"
	"github.com/volatiletech/sqlboiler/v4/queries/qm"
	"strings"
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

type CelCondition struct {
	Field    string `json:"field" validate:"required"`
	Operator string `json:"operator" validate:"required"`
	Value    string `json:"value" validate:"required"`
}

type CelRequestPayload struct {
	Conditions []CelCondition `json:"conditions" validate:"required"`
	Logic      string         `json:"logic" validate:"required"`
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
		ID:                      generateShortID(w.logger),
		Service:                 payload.Service,
		Data:                    payload.Data,
		Trigger:                 payload.Trigger,
		Setup:                   payload.Setup,
		Description:             null.StringFrom(payload.Description),
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
	w.logger.Info().Msg("ListWebhooks endpoint hit")

	devLicense, ok := c.Locals("developer_license_address").([]byte)
	if !ok {
		w.logger.Error().Msg("Developer license not found in request context")
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	events, err := models.Events(
		qm.Where("developer_license_address = ?", devLicense),
		qm.OrderBy("id"),
	).All(c.Context(), w.store.DBS().Reader)

	if err != nil {
		w.logger.Error().Err(err).Msg("Failed to retrieve webhooks")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to retrieve webhooks"})
	}

	w.logger.Info().Int("event_count", len(events)).Msg("Returning webhooks")
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
		Service     string                 `json:"service"`
		Data        string                 `json:"data"`
		Trigger     string                 `json:"trigger"`
		Setup       string                 `json:"setup"`
		TargetURI   string                 `json:"target_uri"`
		Status      string                 `json:"status"`
		Description string                 `json:"description"`
		Parameters  map[string]interface{} `json:"parameters"`
	}

	var payload RequestPayload
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request payload"})
	}

	// Update the webhook fields if provided in the payload
	if payload.Service != "" {
		event.Service = payload.Service
	}
	if payload.Data != "" {
		event.Data = payload.Data
	}
	if payload.Trigger != "" {
		event.Trigger = payload.Trigger
	}
	if payload.Setup != "" {
		event.Setup = payload.Setup
	}
	if payload.TargetURI != "" {
		event.TargetURI = payload.TargetURI
	}
	if payload.Status != "" {
		event.Status = payload.Status
	}
	if payload.Description != "" {
		event.Description = null.StringFrom(payload.Description)
	}
	if payload.Parameters != nil {
		parametersJSON, err := json.Marshal(payload.Parameters)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Failed to serialize parameters"})
		}
		event.Parameters = null.JSONFrom(parametersJSON)
	}

	// Save updates to the database
	if _, err := event.Update(c.Context(), w.store.DBS().Writer, boil.Infer()); err != nil {
		w.logger.Error().Err(err).Msg("Failed to update webhook")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to update webhook"})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"message": "Webhook updated successfully",
		"id":      event.ID,
	})
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

// GetSignalNames godoc
// @Summary      Get signal names
// @Description  Fetches the list of signal names available for the data field.
// @Tags         Webhooks
// @Produce      json
// @Success      200  {array}  string  "List of signal names"
// @Failure      500  "Internal server error"
// @Router       /webhooks/signals [get]
func (w *WebhookController) GetSignalNames(c *fiber.Ctx) error {
	dimoVss := strings.NewReader(schema.VssRel42DIMO())

	vssSignals, err := schema.LoadSignalsCSV(dimoVss)
	if err != nil {
		w.logger.Error().Err(err).Msg("Failed to load VSS signals")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to load signals"})
	}

	signalNames := make([]string, 0)
	for _, vssSignal := range vssSignals {
		if !vssSignal.Deprecated {
			signalNames = append(signalNames, vssSignal.Name)
		}
	}

	w.logger.Info().Int("signal_count", len(signalNames)).Msg("Returning signal names")
	return c.JSON(signalNames)
}

func (w *WebhookController) BuildCEL(c *fiber.Ctx) error {
	var payload CelRequestPayload

	if err := c.BodyParser(&payload); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request payload"})
	}

	// Validate logic
	if payload.Logic != "AND" && payload.Logic != "OR" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid logic. Must be AND or OR."})
	}

	// Generate CEL expression
	conditions := []string{}
	for _, condition := range payload.Conditions {
		expr := fmt.Sprintf("event.%s %s %s", condition.Field, condition.Operator, condition.Value)
		conditions = append(conditions, expr)
	}
	celExpression := strings.Join(conditions, fmt.Sprintf(" %s ", payload.Logic))

	// Return the generated CEL expression
	return c.JSON(fiber.Map{
		"cel_expression": celExpression,
	})
}

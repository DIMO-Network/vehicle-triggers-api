package controllers

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/DIMO-Network/model-garage/pkg/schema"
	"github.com/DIMO-Network/shared/db"
	"github.com/DIMO-Network/vehicle-events-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-events-api/internal/utils"
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
	return strings.TrimSpace(id)
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
// @Description  Registers a new webhook with the specified configuration. The target URI is validated to ensure it is a valid URL, responds with 200 within a timeout, and returns a verification token.
// @Tags         Webhooks
// @Accept       json
// @Produce      json
// @Param        request  body      object  true  "Request payload"
// @Success      201      "Webhook registered successfully"
// @Failure      400      "Invalid request payload or target URI"
// @Failure      500      "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks [post]
func (w *WebhookController) RegisterWebhook(c *fiber.Ctx) error {
	type RequestPayload struct {
		Service           string `json:"service" validate:"required"`
		Data              string `json:"data" validate:"required"`
		Trigger           string `json:"trigger" validate:"required"`
		Setup             string `json:"setup" validate:"required"`
		Description       string `json:"description"`
		TargetURI         string `json:"target_uri" validate:"required"`
		Status            string `json:"status"`
		VerificationToken string `json:"verification_token" validate:"required"`
	}
	var payload RequestPayload
	if err := c.BodyParser(&payload); err != nil {
		w.logger.Error().Err(err).Msg("Invalid request payload")
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request payload"})
	}

	parsedURL, err := url.ParseRequestURI(payload.TargetURI)
	if err != nil {
		w.logger.Error().Err(err).Msg("Invalid target URI")
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid target URI"})
	}

	// --- Begin URI Validation ---
	// Instead of a GET request, we perform a POST with a dummy payload.
	dummyPayload := []byte(`{"verification": "test"}`)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(parsedURL.String(), "application/json", bytes.NewBuffer(dummyPayload))
	if err != nil {
		w.logger.Error().Err(err).Msg("Failed to call target URI")
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Target URI unreachable"})
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		w.logger.Error().Msgf("Target URI responded with status %d", resp.StatusCode)
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": fmt.Sprintf("Target URI did not return status 200 (got %d)", resp.StatusCode)})
	}

	// 3. Verify that the target URI returns the expected verification token.
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		w.logger.Error().Err(err).Msg("Failed to read response from target URI")
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Failed to verify target URI"})
	}

	w.logger.Debug().
		Str("target_uri", parsedURL.String()).
		Str("expected_token", payload.VerificationToken).
		Str("returned_body_raw", string(bodyBytes)).
		Str("returned_body_trimmed", strings.TrimSpace(string(bodyBytes))).
		Msg("URI verification response details")

	responseToken := strings.TrimSpace(string(bodyBytes))
	if responseToken != payload.VerificationToken {
		w.logger.Error().Msgf("Verification token mismatch. Expected '%s', got '%s'", payload.VerificationToken, responseToken)
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Target URI verification failed: token mismatch"})
	}
	// --- End URI Validation ---

	normalized := utils.NormalizeSignalName(payload.Data)

	defaultCooldown := 0
	if payload.Setup == "Hourly" {
		defaultCooldown = 3600
	}

	event := &models.Event{
		ID:                      generateShortID(w.logger),
		Service:                 payload.Service,
		Data:                    normalized,
		Trigger:                 payload.Trigger,
		Setup:                   payload.Setup,
		Description:             null.StringFrom(payload.Description),
		TargetURI:               payload.TargetURI,
		CooldownPeriod:          defaultCooldown,
		DeveloperLicenseAddress: c.Locals("developer_license_address").([]byte),
		Status:                  payload.Status,
	}

	if err := event.Insert(c.Context(), w.store.DBS().Writer, boil.Infer()); err != nil {
		w.logger.Error().Err(err).Msg("Failed to insert webhook into database")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to register webhook"})
	}

	w.logger.Info().Str("id", event.ID).Msg("Webhook registered successfully")
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
// @Security     BearerAuth
// @Router       /v1/webhooks [get]
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

	if events == nil {
		events = make([]*models.Event, 0)
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
// @Param        webhookId       path      string  true  "Webhook ID"
// @Param        request  body      object  true  "Request payload"
// @Success      200      "Webhook updated successfully"
// @Failure      400      "Invalid request payload"
// @Failure      404      "Webhook not found"
// @Failure      500      "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks/{webhookId} [put]
func (w *WebhookController) UpdateWebhook(c *fiber.Ctx) error {
	webhookId := c.Params("webhookId")
	devLicense, ok := c.Locals("developer_license_address").([]byte)
	if !ok {
		w.logger.Error().Msg("Developer license not found in request context")
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	event, err := models.Events(
		qm.Where("id = ? AND developer_license_address = ?", webhookId, devLicense),
	).One(c.Context(), w.store.DBS().Reader)
	if err != nil {
		if err == sql.ErrNoRows {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Webhook not found"})
		}
		w.logger.Error().Err(err).Msg("Failed to retrieve webhook")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to retrieve webhook"})
	}

	type RequestPayload struct {
		Service     string `json:"service"`
		Data        string `json:"data"`
		Trigger     string `json:"trigger"`
		Setup       string `json:"setup"`
		TargetURI   string `json:"target_uri"`
		Status      string `json:"status"`
		Description string `json:"description"`
	}

	var payload RequestPayload
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request payload"})
	}

	if payload.Service != "" {
		event.Service = payload.Service
	}
	if payload.Data != "" {
		event.Data = utils.NormalizeSignalName(payload.Data)
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
	if payload.Trigger != "" {
		event.Trigger = payload.Trigger
	}
	if payload.Description != "" {
		event.Description = null.StringFrom(payload.Description)
	}

	if strings.EqualFold(event.Setup, "Hourly") && event.CooldownPeriod == 0 {
		event.CooldownPeriod = 3600
	}

	if _, err := event.Update(c.Context(), w.store.DBS().Writer, boil.Infer()); err != nil {
		w.logger.Error().Err(err).Msg("Failed to update webhook")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to update webhook"})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{"message": "Webhook updated successfully", "id": event.ID})
}

// DeleteWebhook godoc
// @Summary      Delete a webhook
// @Description  Deletes a webhook by its ID.
// @Tags         Webhooks
// @Produce      json
// @Param        webhookId  path  string  true  "Webhook ID"
// @Success      204  "Webhook deleted successfully"
// @Failure      404  "Webhook not found"
// @Failure      500  "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks/{webhookId} [delete]
func (w *WebhookController) DeleteWebhook(c *fiber.Ctx) error {
	webhookId := c.Params("webhookId")
	devLicense, ok := c.Locals("developer_license_address").([]byte)
	if !ok {
		w.logger.Error().Msg("Developer license not found in request context")
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	event, err := models.Events(
		qm.Where("id = ? AND developer_license_address = ?", webhookId, devLicense),
	).One(c.Context(), w.store.DBS().Reader)
	if err != nil {
		if err == sql.ErrNoRows {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Webhook not found"})
		}
		w.logger.Error().Err(err).Msg("Failed to retrieve webhook")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to retrieve webhook"})
	}

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
// @Security     BearerAuth
// @Router       /v1/webhooks/signals [get]
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

	if payload.Logic != "AND" && payload.Logic != "OR" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid logic. Must be AND or OR."})
	}

	conditions := []string{}
	for _, condition := range payload.Conditions {
		expr := fmt.Sprintf("%s %s %s", condition.Field, condition.Operator, condition.Value)
		conditions = append(conditions, expr)
	}
	celExpression := strings.Join(conditions, fmt.Sprintf(" %s ", payload.Logic))

	return c.JSON(fiber.Map{"cel_expression": celExpression})
}

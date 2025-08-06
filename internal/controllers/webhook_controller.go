package controllers

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/DIMO-Network/model-garage/pkg/schema"
	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/shared/pkg/db"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/volatiletech/null/v8"
	"github.com/volatiletech/sqlboiler/v4/boil"
	"github.com/volatiletech/sqlboiler/v4/queries/qm"
)

type WebhookController struct {
	store  db.Store
	logger zerolog.Logger
}

func NewWebhookController(store db.Store, logger zerolog.Logger) *WebhookController {
	return &WebhookController{
		store:  store,
		logger: logger,
	}
}

type RegisterWebhookRequest struct {
	// Service is the service that the webhook is associated with.
	Service string `json:"service" validate:"required"`
	// MetricName is the name of the metric that the webhook is associated with.
	MetricName string `json:"metricName" validate:"required"`
	// Condition expressed as a CEL expression that needs to be true for the webhook to be triggered.
	Condition string `json:"condition" validate:"required"`
	// CoolDownPeriod is the number of seconds to wait before a webhook can be triggered again.
	// If the webhook is triggered again within the cool down period, the webhook will not be triggered again.
	CoolDownPeriod int `json:"coolDownPeriod" validate:"required"`
	// Description is the description of the webhook.
	Description string `json:"description"`
	// TargetURI is the URI that the webhook will be sent to.
	TargetURI string `json:"targetURI" validate:"required"`
	// Status is the status of the webhook.
	Status string `json:"status"`
	// VerificationToken is the token that wukk
	VerificationToken string `json:"verificationToken" validate:"required"`
}

// RegisterWebhook godoc
// @Summary      Register a new webhook
// @Description  Registers a new webhook with the specified configuration. The target URI is validated to ensure it is a valid URL, responds with 200 within a timeout, and returns a verification token.
// @Tags         Webhooks
// @Accept       json
// @Produce      json
// @Param        request  body      RegisterWebhookRequest  true  "Webhook configuration"
// @Success      201      "Webhook registered successfully"
// @Failure      400      "Invalid request payload or target URI"
// @Failure      500      "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks [post]
func (w *WebhookController) RegisterWebhook(c *fiber.Ctx) error {
	var payload RegisterWebhookRequest
	if err := c.BodyParser(&payload); err != nil {
		return richerrors.Error{
			ExternalMsg: "Invalid request payload",
			Err:         err,
			Code:        fiber.StatusBadRequest,
		}
	}

	parsedURL, err := url.ParseRequestURI(payload.TargetURI)
	if err != nil {
		return richerrors.Error{
			ExternalMsg: "Invalid target URI",
			Err:         err,
			Code:        fiber.StatusBadRequest,
		}
	}

	// --- Begin URI Validation ---
	// Instead of a GET request, we perform a POST with a dummy payload.
	dummyPayload := []byte(`{"verification": "test"}`)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(parsedURL.String(), "application/json", bytes.NewBuffer(dummyPayload))
	if err != nil {
		return richerrors.Error{
			ExternalMsg: "Failed to call target URI",
			Err:         err,
			Code:        fiber.StatusBadRequest,
		}
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return richerrors.Error{
			ExternalMsg: fmt.Sprintf("Target URI did not return status 200 (got %d)", resp.StatusCode),
			Code:        fiber.StatusBadRequest,
		}
	}

	// 3. Verify that the target URI returns the expected verification token.
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return richerrors.Error{
			ExternalMsg: "Failed to read response from target URI",
			Err:         err,
			Code:        fiber.StatusBadRequest,
		}
	}

	responseToken := strings.TrimSpace(string(bodyBytes))
	if responseToken != payload.VerificationToken {
		err := fmt.Errorf("verification token mismatch. Expected '%s', got '%s'", payload.VerificationToken, responseToken)
		return richerrors.Error{
			ExternalMsg: err.Error(),
			Err:         err,
			Code:        fiber.StatusBadRequest,
		}
	}
	// --- End URI Validation ---

	event := &models.Trigger{
		ID:                      uuid.New().String(),
		Service:                 payload.Service,
		MetricName:              payload.MetricName,
		Condition:               payload.Condition,
		Description:             null.StringFrom(payload.Description),
		TargetURI:               payload.TargetURI,
		CooldownPeriod:          payload.CoolDownPeriod,
		DeveloperLicenseAddress: c.Locals("developer_license_address").([]byte),
		Status:                  payload.Status,
	}

	if err := event.Insert(c.Context(), w.store.DBS().Writer, boil.Infer()); err != nil {
		return richerrors.Error{
			ExternalMsg: "Failed to add webhook",
			Err:         err,
			Code:        fiber.StatusInternalServerError,
		}
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
		return richerrors.Error{
			ExternalMsg: "Developer license not found in request",
			Err:         fmt.Errorf("developer license not found in request context"),
			Code:        fiber.StatusInternalServerError,
		}
	}

	events, err := models.Triggers(
		models.TriggerWhere.DeveloperLicenseAddress.EQ(devLicense),
		qm.OrderBy("id"),
	).All(c.Context(), w.store.DBS().Reader)

	if err != nil {
		return richerrors.Error{
			ExternalMsg: "Failed to retrieve webhooks",
			Err:         err,
			Code:        fiber.StatusInternalServerError,
		}
	}

	if events == nil {
		events = make([]*models.Trigger, 0)
	}

	return c.JSON(events)
}

// UpdateWebhookRequest is the request payload for updating a webhook.
type UpdateWebhookRequest struct {
	Service        *string `json:"service"`
	MetricName     *string `json:"metricName"`
	Condition      *string `json:"condition"`
	CoolDownPeriod *int    `json:"coolDownPeriod"`
	TargetURI      *string `json:"targetURI"`
	Status         *string `json:"status"`
	Description    *string `json:"description"`
}

// UpdateWebhook godoc
// @Summary      Update a webhook
// @Description  Updates the configuration of a webhook by its ID.
// @Tags         Webhooks
// @Accept       json
// @Produce      json
// @Param        webhookId       path      string  true  "Webhook ID"
// @Param        request  body      UpdateWebhookRequest  true  "Webhook configuration"
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
		return richerrors.Error{
			ExternalMsg: "Developer license not found in request",
			Err:         fmt.Errorf("developer license not found in request context"),
			Code:        fiber.StatusInternalServerError,
		}
	}

	event, err := models.Triggers(
		models.TriggerWhere.ID.EQ(webhookId),
		models.TriggerWhere.DeveloperLicenseAddress.EQ(devLicense),
	).One(c.Context(), w.store.DBS().Reader)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return richerrors.Error{
				ExternalMsg: "Webhook not found",
				Code:        fiber.StatusNotFound,
			}
		}
		return richerrors.Error{
			ExternalMsg: "Failed to retrieve webhook",
			Err:         err,
			Code:        fiber.StatusInternalServerError,
		}
	}

	var payload UpdateWebhookRequest
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request payload"})
	}

	if payload.Service != nil {
		event.Service = *payload.Service
	}
	if payload.MetricName != nil {
		event.MetricName = *payload.MetricName
	}
	if payload.TargetURI != nil {
		event.TargetURI = *payload.TargetURI
	}
	if payload.Status != nil {
		event.Status = *payload.Status
	}
	if payload.Condition != nil {
		event.Condition = *payload.Condition
	}
	if payload.Description != nil {
		event.Description = null.StringFrom(*payload.Description)
	}
	if payload.CoolDownPeriod != nil {
		event.CooldownPeriod = *payload.CoolDownPeriod
	}

	if _, err := event.Update(c.Context(), w.store.DBS().Writer, boil.Infer()); err != nil {
		return richerrors.Error{
			ExternalMsg: "Failed to update webhook",
			Err:         err,
			Code:        fiber.StatusInternalServerError,
		}
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
		return richerrors.Error{
			ExternalMsg: "Developer license not found in request",
			Err:         fmt.Errorf("developer license not found in request context"),
			Code:        fiber.StatusInternalServerError,
		}
	}

	_ = devLicense // TODO(kevin): verify that the developer license is the owner of the webhook

	event, err := models.Triggers(
		models.TriggerWhere.ID.EQ(webhookId),
		models.TriggerWhere.DeveloperLicenseAddress.EQ(devLicense),
	).One(c.Context(), w.store.DBS().Reader)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return richerrors.Error{
				ExternalMsg: "Webhook not found",
				Code:        fiber.StatusNotFound,
			}
		}
		return richerrors.Error{
			ExternalMsg: "Failed to retrieve webhook",
			Err:         err,
			Code:        fiber.StatusInternalServerError,
		}
	}

	if _, err := event.Delete(c.Context(), w.store.DBS().Writer); err != nil {
		return richerrors.Error{
			ExternalMsg: "Failed to delete webhook",
			Err:         err,
			Code:        fiber.StatusInternalServerError,
		}
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{"message": "Webhook deleted successfully"})
}

type SignalDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Unit        string `json:"unit"`
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
	defs, err := schema.LoadDefinitionFile(strings.NewReader(schema.DefaultDefinitionsYAML()))
	if err != nil {
		return richerrors.Error{
			ExternalMsg: "Failed to load default schema definitions",
			Err:         err,
			Code:        fiber.StatusInternalServerError,
		}
	}
	signalInfo, err := schema.LoadSignalsCSV(strings.NewReader(schema.VssRel42DIMO()))
	if err != nil {
		return richerrors.Error{
			ExternalMsg: "Failed to load default signal info",
			Err:         err,
			Code:        fiber.StatusInternalServerError,
		}
	}
	definedSignals := defs.DefinedSignal(signalInfo)
	signalDefs := make([]SignalDefinition, 0, len(definedSignals))
	for _, signal := range definedSignals {
		signalDefs = append(signalDefs, SignalDefinition{
			Name:        signal.JSONName,
			Unit:        signal.Unit,
			Description: signal.Desc,
		})
	}
	return c.JSON(signalDefs)
}

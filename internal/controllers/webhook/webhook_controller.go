package webhook

import (
	"context"
	"fmt"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/auth"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/configaudit"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/signals"
	"github.com/aarondl/null/v8"
	"github.com/ethereum/go-ethereum/common"
	"github.com/gofiber/fiber/v2"
)

type Repository interface {
	CreateTrigger(ctx context.Context, req triggersrepo.CreateTriggerRequest) (*models.Trigger, error)
	GetTriggersByDeveloperLicense(ctx context.Context, developerLicense common.Address) ([]*models.Trigger, error)
	GetTriggerByIDAndDeveloperLicense(ctx context.Context, triggerID string, developerLicense common.Address) (*models.Trigger, error)
	UpdateTrigger(ctx context.Context, trigger *models.Trigger) error
	DeleteTrigger(ctx context.Context, triggerID string, developerLicense common.Address) error

	// subscriptions
	CreateVehicleSubscription(ctx context.Context, assetDID cloudevent.ERC721DID, triggerID string) (*models.VehicleSubscription, error)
	GetVehicleSubscriptionsByTriggerID(ctx context.Context, triggerID string) ([]*models.VehicleSubscription, error)
	GetVehicleSubscriptionsByVehicleAndDeveloperLicense(ctx context.Context, assetDID cloudevent.ERC721DID, developerLicense common.Address) ([]*models.VehicleSubscription, error)
	DeleteVehicleSubscription(ctx context.Context, triggerID string, assetDID cloudevent.ERC721DID) (int64, error)
	DeleteAllVehicleSubscriptionsForTrigger(ctx context.Context, triggerID string) (int64, error)
}

type WebhookCache interface {
	ScheduleRefresh(ctx context.Context)
}

// ConfigAudit publishes config-change events for downstream compliance and
// change-management. Best-effort: failures are logged, never block the API.
type ConfigAudit interface {
	Publish(ctx context.Context, e configaudit.Event) error
}

// WebhookController is the controller for creating and managing webhooks.
type WebhookController struct {
	repo              Repository
	signalDefs        []signals.SignalDefinition
	cache             WebhookCache
	audit             ConfigAudit
	maxCooldownPeriod int
}

// NewWebhookController creates a new WebhookController. maxCooldownPeriod
// caps trigger.cooldown_period at registration; the cap is required so the
// trigger_state KV bucket TTL can be sized to cover any allowed cooldown
// (enforced by Settings.Validate).
func NewWebhookController(repo Repository, cache WebhookCache, maxCooldownPeriod int) (*WebhookController, error) {
	return &WebhookController{
		repo:              repo,
		signalDefs:        signals.GetAllSignalDefinitions(),
		cache:             cache,
		audit:             configaudit.Noop{},
		maxCooldownPeriod: maxCooldownPeriod,
	}, nil
}

// WithAudit wires the controller to publish config-change events. Returns
// the same controller for fluent chaining at construction time.
func (w *WebhookController) WithAudit(a ConfigAudit) *WebhookController {
	if a == nil {
		a = configaudit.Noop{}
	}
	w.audit = a
	return w
}

// publishAudit emits a config-change event for the audit trail. Failures are
// logged via the audit publisher's own logging; the controller continues.
func (w *WebhookController) publishAudit(ctx context.Context, op configaudit.Op, webhookID, devLicense string, snapshot map[string]any) {
	if w.audit == nil {
		return
	}
	_ = w.audit.Publish(ctx, configaudit.Event{
		Op:         op,
		WebhookID:  webhookID,
		DevLicense: devLicense,
		Snapshot:   snapshot,
	})
}

// RegisterWebhook godoc
// @Summary      Register a new webhook
// @Description  Registers a new webhook with the specified configuration. The target URI is validated to ensure it is a valid URL, responds with 200 within a timeout, and returns a verification token.
// @Tags         Webhooks
// @Accept       json
// @Produce      json
// @Param        request  body      RegisterWebhookRequest     true  "Webhook configuration"
// @Success      201      {object}  RegisterWebhookResponse    "Webhook registered successfully"
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

	if err := validateTargetURL(payload.TargetURL); err != nil {
		return err
	}

	if err := validateServiceAndMetricNameAndCondition(payload.Service, payload.MetricName, payload.Condition); err != nil {
		return err
	}

	if err := validateCoolDownPeriod(payload.CoolDownPeriod, w.maxCooldownPeriod); err != nil {
		return err
	}

	if err := validateStatus(payload.Status); err != nil {
		return err
	}

	if err := verifyWebhookURL(c.Context(), payload.TargetURL, payload.VerificationToken); err != nil {
		return err
	}

	token, err := auth.GetDexJWT(c)
	if err != nil {
		return err
	}

	req := triggersrepo.CreateTriggerRequest{
		Service:                 payload.Service,
		MetricName:              payload.MetricName,
		Condition:               payload.Condition,
		TargetURI:               payload.TargetURL,
		Status:                  payload.Status,
		Description:             payload.Description,
		CooldownPeriod:          payload.CoolDownPeriod,
		DeveloperLicenseAddress: token.EthereumAddress,
		DisplayName:             payload.DisplayName,
	}

	trigger, err := w.repo.CreateTrigger(c.Context(), req)
	if err != nil {
		return richerrors.Error{
			ExternalMsg: "Failed to add webhook",
			Err:         err,
			Code:        fiber.StatusInternalServerError,
		}
	}

	secret := ""
	if trigger.SigningSecret.Valid {
		secret = trigger.SigningSecret.String
	}
	w.publishAudit(c.Context(), configaudit.OpWebhookCreate, trigger.ID, token.EthereumAddress.Hex(), map[string]any{
		"service":    trigger.Service,
		"metricName": trigger.MetricName,
		"targetUri":  trigger.TargetURI,
		"status":     trigger.Status,
		"cooldown":   trigger.CooldownPeriod,
	})
	return c.Status(fiber.StatusCreated).JSON(RegisterWebhookResponse{
		ID:                 trigger.ID,
		Message:            "Webhook registered successfully",
		SigningSecret:      secret,
		SignatureAlgorithm: "HMAC-SHA256(timestamp + \".\" + body)",
	})
}

// ListWebhooks godoc
// @Summary      List all webhooks
// @Description  Retrieves all registered webhooks for the developer.
// @Tags         Webhooks
// @Produce      json
// @Success      200  {array}  WebhookView  "List of webhooks"
// @Failure      401  "Unauthorized"
// @Failure      500  "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks [get]
func (w *WebhookController) ListWebhooks(c *fiber.Ctx) error {
	devLicense, err := getDevLicense(c)
	if err != nil {
		return err
	}

	triggers, err := w.repo.GetTriggersByDeveloperLicense(c.Context(), devLicense)
	if err != nil {
		return fmt.Errorf("failed to retrieve webhooks: %w", err)
	}

	out := make([]WebhookView, 0, len(triggers))
	for _, t := range triggers {
		desc := ""
		if t.Description.Valid {
			desc = t.Description.String
		}
		out = append(out, WebhookView{
			ID:             t.ID,
			Service:        t.Service,
			MetricName:     t.MetricName,
			Condition:      t.Condition,
			TargetURL:      t.TargetURI,
			CoolDownPeriod: t.CooldownPeriod,
			Status:         t.Status,
			Description:    desc,
			CreatedAt:      t.CreatedAt,
			UpdatedAt:      t.UpdatedAt,
			FailureCount:   t.FailureCount,
			DisplayName:    t.DisplayName,
		})
	}
	return c.JSON(out)
}

// UpdateWebhook godoc
// @Summary      Update a webhook
// @Description  Updates the configuration of a webhook by its ID. The failure count is reset to 0 when updating a webhook.
// @Tags         Webhooks
// @Accept       json
// @Produce      json
// @Param        webhookId       path      string  true  "Webhook ID"
// @Param        request  body      UpdateWebhookRequest   true  "Webhook configuration"
// @Success      200      {object}  UpdateWebhookResponse  "Webhook updated successfully"
// @Failure      400      "Invalid request payload"
// @Failure      404      "Webhook not found"
// @Failure      500      "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks/{webhookId} [put]
func (w *WebhookController) UpdateWebhook(c *fiber.Ctx) error {
	webhookId := c.Params("webhookId")
	devLicense, err := getDevLicense(c)
	if err != nil {
		return err
	}

	event, err := w.repo.GetTriggerByIDAndDeveloperLicense(c.Context(), webhookId, devLicense)
	if err != nil {
		return fmt.Errorf("failed to retrieve webhook: %w", err)
	}

	var payload UpdateWebhookRequest
	if err := c.BodyParser(&payload); err != nil {
		return richerrors.Error{
			ExternalMsg: "Invalid request payload: " + err.Error(),
			Err:         err,
			Code:        fiber.StatusBadRequest,
		}
	}

	if payload.TargetURL != nil {
		if err := validateTargetURL(*payload.TargetURL); err != nil {
			return err
		}
		event.TargetURI = *payload.TargetURL
	}
	if payload.Status != nil {
		if err := validateStatus(*payload.Status); err != nil {
			return err
		}
		event.Status = *payload.Status
	}
	if payload.Condition != nil {
		if err := validateServiceAndMetricNameAndCondition(event.Service, event.MetricName, *payload.Condition); err != nil {
			return err
		}
		event.Condition = *payload.Condition
	}
	if payload.CoolDownPeriod != nil {
		if err := validateCoolDownPeriod(*payload.CoolDownPeriod, w.maxCooldownPeriod); err != nil {
			return err
		}
		event.CooldownPeriod = *payload.CoolDownPeriod
	}
	if payload.Description != nil {
		event.Description = null.StringFrom(*payload.Description)
	}
	if payload.DisplayName != nil {
		event.DisplayName = *payload.DisplayName
	}
	// Always reset failure count to 0 when updating a webhook
	event.FailureCount = 0

	if err := w.repo.UpdateTrigger(c.Context(), event); err != nil {
		return fmt.Errorf("failed to update webhook: %w", err)
	}

	w.cache.ScheduleRefresh(c.Context())
	w.publishAudit(c.Context(), configaudit.OpWebhookUpdate, event.ID, common.BytesToAddress(event.DeveloperLicenseAddress).Hex(), map[string]any{
		"status":    event.Status,
		"targetUri": event.TargetURI,
		"condition": event.Condition,
		"cooldown":  event.CooldownPeriod,
	})

	return c.Status(fiber.StatusOK).JSON(UpdateWebhookResponse{ID: event.ID, Message: "Webhook updated successfully"})
}

// DeleteWebhook godoc
// @Summary      Delete a webhook
// @Description  Deletes a webhook by its ID.
// @Tags         Webhooks
// @Produce      json
// @Param        webhookId  path  string  true  "Webhook ID"
// @Success      200  {object}  GenericResponse  "Webhook deleted successfully"
// @Failure      404  "Webhook not found"
// @Failure      500  "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks/{webhookId} [delete]
func (w *WebhookController) DeleteWebhook(c *fiber.Ctx) error {
	webhookID, err := getWebhookID(c)
	if err != nil {
		return err
	}
	devLicense, err := getDevLicense(c)
	if err != nil {
		return err
	}

	_, err = ownerCheck(c.Context(), w.repo, webhookID, devLicense)
	if err != nil {
		return err
	}

	if err := w.repo.DeleteTrigger(c.Context(), webhookID, devLicense); err != nil {
		return fmt.Errorf("failed to delete webhook: %w", err)
	}
	w.cache.ScheduleRefresh(c.Context())
	w.publishAudit(c.Context(), configaudit.OpWebhookDelete, webhookID, devLicense.Hex(), nil)

	return c.Status(fiber.StatusOK).JSON(GenericResponse{Message: "Webhook deleted successfully"})
}

// GetSignalNames godoc
// @Summary      Get signal names
// @Description  Fetches the list of signal names available for the data field.
// @Tags         Webhooks
// @Produce      json
// @Success      200  {array}  signals.SignalDefinition  "List of signal names"
// @Failure      500  "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks/signals [get]
func (w *WebhookController) GetSignalNames(c *fiber.Ctx) error {
	return c.JSON(w.signalDefs)
}

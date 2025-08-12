package webhook

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/DIMO-Network/model-garage/pkg/schema"
	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/auth"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/ethereum/go-ethereum/common"
	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog"
	"github.com/volatiletech/null/v8"
)

type Repository interface {
	CreateTrigger(ctx context.Context, req triggersrepo.CreateTriggerRequest) (*models.Trigger, error)
	GetTriggersByDeveloperLicense(ctx context.Context, developerLicense common.Address) ([]*models.Trigger, error)
	GetTriggerByIDAndDeveloperLicense(ctx context.Context, triggerID string, developerLicense common.Address) (*models.Trigger, error)
	UpdateTrigger(ctx context.Context, trigger *models.Trigger) error
	DeleteTrigger(ctx context.Context, triggerID string, developerLicense common.Address) error

	// subscriptions
	CreateVehicleSubscription(ctx context.Context, tokenID *big.Int, triggerID string) (*models.VehicleSubscription, error)
	GetVehicleSubscriptionsByTriggerID(ctx context.Context, triggerID string) ([]*models.VehicleSubscription, error)
	GetVehicleSubscriptionsByVehicleAndDeveloperLicense(ctx context.Context, tokenID *big.Int, developerLicense common.Address) ([]*models.VehicleSubscription, error)
	DeleteVehicleSubscription(ctx context.Context, triggerID string, tokenID *big.Int) (int64, error)
	DeleteAllVehicleSubscriptionsForTrigger(ctx context.Context, triggerID string) (int64, error)

	GetWebhookOwner(ctx context.Context, webhookID string) (common.Address, error)
}

type WebhookCache interface {
	PopulateCache(ctx context.Context) error
}

// WebhookController is the controller for creating and managing webhooks.
type WebhookController struct {
	repo       Repository
	signalDefs []SignalDefinition
	cache      WebhookCache
}

// NewWebhookController creates a new WebhookController.
func NewWebhookController(repo Repository, cache WebhookCache) (*WebhookController, error) {
	signalDefs, err := loadSignalDefs()
	if err != nil {
		return nil, fmt.Errorf("failed to load signal definitions: %w", err)
	}
	return &WebhookController{
		repo:       repo,
		signalDefs: signalDefs,
		cache:      cache,
	}, nil
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

	if err := w.validateServiceAndMetricName(payload.Service, payload.MetricName); err != nil {
		return err
	}

	if err := validateCondition(payload.Condition); err != nil {
		return err
	}

	if err := validateCoolDownPeriod(payload.CoolDownPeriod); err != nil {
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

	return c.Status(fiber.StatusCreated).JSON(RegisterWebhookResponse{ID: trigger.ID, Message: "Webhook registered successfully"})
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

	if payload.MetricName != nil {
		if err := w.validateServiceAndMetricName(event.Service, *payload.MetricName); err != nil {
			return err
		}
		event.MetricName = *payload.MetricName
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
		if err := validateCondition(*payload.Condition); err != nil {
			return err
		}
		event.Condition = *payload.Condition
	}
	if payload.CoolDownPeriod != nil {
		if err := validateCoolDownPeriod(*payload.CoolDownPeriod); err != nil {
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

	if err := w.cache.PopulateCache(c.Context()); err != nil {
		zerolog.Ctx(c.UserContext()).Error().Err(err).Msg("Failed to populate cache after updating webhook")
	}

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

	err = ownerCheck(c.Context(), w.repo, webhookID, devLicense)
	if err != nil {
		return err
	}

	if err := w.repo.DeleteTrigger(c.Context(), webhookID, devLicense); err != nil {
		return fmt.Errorf("failed to delete webhook: %w", err)
	}
	if err := w.cache.PopulateCache(c.Context()); err != nil {
		zerolog.Ctx(c.UserContext()).Error().Err(err).Msg("Failed to populate cache after deleting webhook")
	}

	return c.Status(fiber.StatusOK).JSON(GenericResponse{Message: "Webhook deleted successfully"})
}

// GetSignalNames godoc
// @Summary      Get signal names
// @Description  Fetches the list of signal names available for the data field.
// @Tags         Webhooks
// @Produce      json
// @Success      200  {array}  SignalDefinition  "List of signal names"
// @Failure      500  "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks/signals [get]
func (w *WebhookController) GetSignalNames(c *fiber.Ctx) error {
	return c.JSON(w.signalDefs)
}

func loadSignalDefs() ([]SignalDefinition, error) {
	defs, err := schema.LoadDefinitionFile(strings.NewReader(schema.DefaultDefinitionsYAML()))
	if err != nil {
		return nil, fmt.Errorf("failed to load default schema definitions: %w", err)
	}
	signalInfo, err := schema.LoadSignalsCSV(strings.NewReader(schema.VssRel42DIMO()))
	if err != nil {
		return nil, fmt.Errorf("failed to load default signal info: %w", err)
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
	return signalDefs, nil
}

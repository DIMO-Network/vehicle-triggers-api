package webhook

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"net/http"

	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/clients/identity"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/clients/tokenexchange"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhookcache"
	"github.com/ethereum/go-ethereum/common"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// VehicleSubscriptionController is a controller for creating, and managing vehicle subscriptions.
type VehicleSubscriptionController struct {
	identityAPI         *identity.Client
	tokenExchangeClient *tokenexchange.Client
	cache               *webhookcache.WebhookCache
	repo                *triggersrepo.Repository
}

// NewVehicleSubscriptionController creates a new VehicleSubscriptionController.
func NewVehicleSubscriptionController(repo *triggersrepo.Repository, identityAPI *identity.Client, tokenExchangeClient *tokenexchange.Client, cache *webhookcache.WebhookCache) *VehicleSubscriptionController {
	return &VehicleSubscriptionController{
		identityAPI:         identityAPI,
		tokenExchangeClient: tokenExchangeClient,
		cache:               cache,
		repo:                repo,
	}
}

// AssignVehicleToWebhook godoc
// @Summary      Assign a vehicle to a webhook
// @Description  Associates a vehicle with a specific event webhook.
// @Tags         Webhooks
// @Accept       json
// @Produce      json
// @Param        webhookId       path      string  true  "Webhook ID"
// @Param        vehicleTokenId  path      string  true  "Vehicle Token ID"
// @Success      201             {object}  GenericResponse  "Vehicle assigned"
// @Failure      400             {object}  map[string]string  "Bad request"
// @Failure      401             {object}  map[string]string  "Unauthorized"
// @Failure      500             {object}  map[string]string  "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks/{webhookId}/subscribe/{vehicleTokenId} [post]
func (v *VehicleSubscriptionController) AssignVehicleToWebhook(c *fiber.Ctx) error {
	webhookID, err := getWebhookID(c)
	if err != nil {
		return err
	}
	tokenID, err := getVehicleTokenID(c)
	if err != nil {
		return err
	}

	dl, err := getDevLicense(c)
	if err != nil {
		return err
	}
	if err := ownerCheck(c.Context(), v.repo, webhookID, dl); err != nil {
		return err
	}

	hasPerm, err := v.tokenExchangeClient.HasVehiclePermissions(c.Context(), tokenID, dl, []string{
		"privilege:GetNonLocationHistory",
		"privilege:GetLocationHistory",
	})
	if err != nil {
		return richerrors.Error{
			ExternalMsg: "Failed to validate permissions",
			Err:         err,
			Code:        fiber.StatusInternalServerError,
		}
	}
	if !hasPerm {
		return richerrors.Error{
			ExternalMsg: "Insufficient vehicle permissions",
			Code:        fiber.StatusForbidden,
		}
	}

	_, err = v.repo.CreateVehicleSubscription(c.Context(), tokenID, webhookID)
	if err != nil {
		return fmt.Errorf("failed to assign vehicle: %w", err)
	}

	_ = v.cache.PopulateCache(c.Context())
	return c.Status(http.StatusCreated).JSON(GenericResponse{Message: "Vehicle assigned successfully"})
}

// SubscribeVehiclesFromList godoc
// @Summary      Assign multiple vehicles to a webhook from a list
// @Description  Subscribes each vehicleTokenId to the webhook.
// @Tags         Webhooks
// @Accept       json
// @Produce      json
// @Param        webhookId  path  string  true  "Webhook ID"
// @Success      201        {object}  GenericResponse  "Count of subscribed vehicles"
// @Failure      400        {object}  map[string]string  "Bad request"
// @Failure      401        {object}  map[string]string  "Unauthorized"
// @Failure      500        {object}  map[string]string  "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks/{webhookId}/subscribe/list [post]
func (v *VehicleSubscriptionController) SubscribeVehiclesFromList(c *fiber.Ctx) error {
	webhookID, err := getWebhookID(c)
	if err != nil {
		return err
	}
	dl, err := getDevLicense(c)
	if err != nil {
		return err
	}

	err = ownerCheck(c.Context(), v.repo, webhookID, dl)
	if err != nil {
		return err
	}

	var req VehicleListRequest
	if err := c.BodyParser(&req); err != nil {
		return richerrors.Error{
			ExternalMsg: "Invalid request body",
			Err:         err,
			Code:        http.StatusBadRequest,
		}
	}

	return v.subscribeMultipleVehiclesToWebhook(c, webhookID, dl, req.VehicleTokenIDs)
}

// UnsubscribeVehiclesFromList godoc
// @Summary      Unsubscribe multiple vehicles from a webhook using a list
// @Description  Unsubscribes each vehicleTokenId from the webhook.
// @Tags         Webhooks
// @Accept       json
// @Produce      json
// @Param        webhookId  path  string  true  "Webhook ID"
// @Success      200        {object}  GenericResponse  "Count of unsubscribed vehicles"
// @Failure      400        {object}  map[string]string  "Bad request"
// @Failure      401        {object}  map[string]string  "Unauthorized"
// @Failure      500        {object}  map[string]string  "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks/{webhookId}/unsubscribe/list [delete]
func (v *VehicleSubscriptionController) UnsubscribeVehiclesFromList(c *fiber.Ctx) error {
	webhookID, err := getWebhookID(c)
	if err != nil {
		return err
	}
	dl, err := getDevLicense(c)
	if err != nil {
		return err
	}
	err = ownerCheck(c.Context(), v.repo, webhookID, dl)
	if err != nil {
		return err
	}

	var req VehicleListRequest
	if err := c.BodyParser(&req); err != nil {
		return richerrors.Error{
			ExternalMsg: "Invalid request body",
			Err:         err,
			Code:        http.StatusBadRequest,
		}
	}

	var failedUnSubscriptions []FailedSubscription

	for _, tokenID := range req.VehicleTokenIDs {
		_, err := v.repo.DeleteVehicleSubscription(c.Context(), webhookID, tokenID)
		if err != nil {
			errMsg := "failed to unsubscribe vehicle"
			if richErr, ok := richerrors.AsRichError(err); ok {
				errMsg = richErr.ExternalMsg
			}
			failedUnSubscriptions = append(failedUnSubscriptions, FailedSubscription{
				VehicleTokenID: tokenID,
				Message:        errMsg,
			})
		}
	}

	if len(failedUnSubscriptions) > 0 {
		return c.JSON(FailedSubscriptionResponse{FailedSubscriptions: failedUnSubscriptions})
	}
	if err := v.cache.PopulateCache(c.Context()); err != nil {
		zerolog.Ctx(c.UserContext()).Error().Err(err).Msg("Failed to populate cache after subscribing vehicles")
	}
	return c.JSON(GenericResponse{Message: fmt.Sprintf("Unsubscribed %d vehicles", len(req.VehicleTokenIDs)-len(failedUnSubscriptions))})

}

// RemoveVehicleFromWebhook godoc
// @Summary      Unsubscribe a vehicle from a webhook
// @Description  Removes a vehicle's subscription.
// @Tags         Webhooks
// @Produce      json
// @Param        webhookId       path  string  true  "Webhook ID"
// @Param        vehicleTokenId  path  string  true  "Vehicle Token ID"
// @Success      200             {object}  GenericResponse  "Vehicle removed"
// @Failure      400             {object}  map[string]string  "Bad request"
// @Failure      401             {object}  map[string]string  "Unauthorized"
// @Failure      500             {object}  map[string]string  "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks/{webhookId}/unsubscribe/{vehicleTokenId} [delete]
func (v *VehicleSubscriptionController) RemoveVehicleFromWebhook(c *fiber.Ctx) error {
	webhookID, err := getWebhookID(c)
	if err != nil {
		return err
	}
	tokenID, err := getVehicleTokenID(c)
	if err != nil {
		return err
	}
	dl, err := getDevLicense(c)
	if err != nil {
		return err
	}
	err = ownerCheck(c.Context(), v.repo, webhookID, dl)
	if err != nil {
		return err
	}

	_, err = v.repo.DeleteVehicleSubscription(c.Context(), webhookID, tokenID)
	if err != nil {
		return richerrors.Error{ExternalMsg: "Failed to unsubscribe", Err: err, Code: http.StatusInternalServerError}
	}
	return c.JSON(GenericResponse{Message: "Vehicle unsubscribed successfully"})
}

// SubscribeAllVehiclesToWebhook godoc
// @Summary      Subscribe all shared vehicles
// @Description  Subscribes every vehicle shared with this developer to the webhook.
// @Tags         Webhooks
// @Produce      json
// @Param        webhookId  path  string  true  "Webhook ID"
// @Success      201        {object}  GenericResponse  "Count of subscribed vehicles"
// @Failure      400        {object}  map[string]string  "Bad request"
// @Failure      401        {object}  map[string]string  "Unauthorized"
// @Failure      500        {object}  map[string]string  "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks/{webhookId}/subscribe/all [post]
func (v *VehicleSubscriptionController) SubscribeAllVehiclesToWebhook(c *fiber.Ctx) error {
	webhookID, err := getWebhookID(c)
	if err != nil {
		return err
	}
	dl, err := getDevLicense(c)
	if err != nil {
		return err
	}
	err = ownerCheck(c.Context(), v.repo, webhookID, dl)
	if err != nil {
		return err
	}

	vehicles, err := v.identityAPI.GetSharedVehicles(c.Context(), dl.Bytes())
	if err != nil {
		return fmt.Errorf("failed to fetch shared vehicles: %w", err)
	}

	return v.subscribeMultipleVehiclesToWebhook(c, webhookID, dl, vehicles)
}

// UnsubscribeAllVehiclesFromWebhook godoc
// @Summary      Unsubscribe all shared vehicles
// @Description  Removes every shared vehicle subscription for this webhook.
// @Tags         Webhooks
// @Produce      json
// @Param        webhookId  path  string  true  "Webhook ID"
// @Success      200        {object}  GenericResponse  "Count of unsubscribed vehicles"
// @Failure      400        {object}  map[string]string  "Bad request"
// @Failure      401        {object}  map[string]string  "Unauthorized"
// @Failure      500        {object}  map[string]string  "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks/{webhookId}/unsubscribe/all [delete]
func (v *VehicleSubscriptionController) UnsubscribeAllVehiclesFromWebhook(c *fiber.Ctx) error {
	webhookID, err := getWebhookID(c)
	if err != nil {
		return err
	}
	dl, err := getDevLicense(c)
	if err != nil {
		return err
	}
	if err := ownerCheck(c.Context(), v.repo, webhookID, dl); err != nil {
		return err
	}

	res, err := v.repo.DeleteAllVehicleSubscriptionsForTrigger(c.Context(), webhookID)
	if err != nil {
		return fmt.Errorf("failed to unsubscribe all vehicles: %w", err)
	}
	return c.JSON(GenericResponse{Message: fmt.Sprintf("Unsubscribed %d vehicles", res)})
}

// ListSubscriptions godoc
// @Summary      List subscriptions for a vehicle
// @Description  Retrieves all webhook subscriptions for a given vehicle.
// @Tags         Webhooks
// @Produce      json
// @Param        vehicleTokenId path string true "Vehicle Token ID"
// @Success      200            {array}   SubscriptionView
// @Failure      401            {object}  map[string]string  "Unauthorized"
// @Failure      500            {object}  map[string]string  "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks/vehicles/{vehicleTokenId} [get]
func (v *VehicleSubscriptionController) ListSubscriptions(c *fiber.Ctx) error {
	tokenID, err := getVehicleTokenID(c)
	if err != nil {
		return err
	}
	dl, err := getDevLicense(c)
	if err != nil {
		return err
	}

	subs, err := v.repo.GetVehicleSubscriptionsByVehicleAndDeveloperLicense(c.Context(), tokenID, dl)
	if err != nil {
		return fmt.Errorf("failed to fetch subscriptions: %w", err)
	}

	out := make([]SubscriptionView, 0, len(subs))
	for _, s := range subs {
		desc := ""
		if s.R != nil && s.R.Trigger != nil {
			desc = s.R.Trigger.Description.String
		}
		out = append(out, SubscriptionView{
			WebhookID:      s.TriggerID,
			VehicleTokenID: s.VehicleTokenID.String(),
			CreatedAt:      s.CreatedAt,
			Description:    desc,
		})
	}
	return c.JSON(out)
}

// ListVehiclesForWebhook godoc
// @Summary      List all vehicles subscribed to a webhook
// @Description  Returns every vehicleTokenId currently subscribed.
// @Tags         Webhooks
// @Produce      json
// @Param        webhookId  path  string  true  "Webhook ID"
// @Success      200        {array}   string
// @Failure      401        {object}  map[string]string  "Unauthorized"
// @Failure      500        {object}  map[string]string  "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks/{webhookId} [get]
func (v *VehicleSubscriptionController) ListVehiclesForWebhook(c *fiber.Ctx) error {
	webhookID, err := getWebhookID(c)
	if err != nil {
		return err
	}
	dl, err := getDevLicense(c)
	if err != nil {
		return err
	}

	err = ownerCheck(c.Context(), v.repo, webhookID, dl)
	if err != nil {
		return err
	}

	subs, err := v.repo.GetVehicleSubscriptionsByTriggerID(c.Context(), webhookID)
	if err != nil {
		return fmt.Errorf("failed to get vehicle subscriptions: %w", err)
	}

	ids := make([]string, len(subs))
	for i, s := range subs {
		ids[i] = s.VehicleTokenID.String()
	}
	return c.JSON(ids)
}

func ownerCheck(ctx context.Context, repo *triggersrepo.Repository, webhookID string, developerLicense common.Address) error {
	owner, err := repo.GetWebhookOwner(ctx, webhookID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return richerrors.Error{
				ExternalMsg: "Webhook not found",
				Code:        fiber.StatusNotFound,
			}
		}
		return richerrors.Error{
			ExternalMsg: "Failed to fetch webhook",
			Err:         err,
			Code:        fiber.StatusInternalServerError,
		}
	}
	if owner != developerLicense {
		return richerrors.Error{
			ExternalMsg: "Webhook not found",
			Err:         fmt.Errorf("developer license %s is not the owner of webhook %s", developerLicense.Hex(), webhookID),
			Code:        fiber.StatusNotFound,
		}
	}
	return nil
}

func getVehicleTokenID(c *fiber.Ctx) (*big.Int, error) {
	tokenStr := c.Params("vehicleTokenId")
	if tokenStr == "" {
		return nil, richerrors.Error{
			ExternalMsg: "Vehicle token Id path parameter can not be empty",
			Code:        http.StatusBadRequest,
		}
	}
	tokenID := new(big.Int)
	if _, ok := tokenID.SetString(tokenStr, 10); !ok {
		return nil, richerrors.Error{
			ExternalMsg: "Invalid vehicle token ID",
			Err:         fmt.Errorf("invalid vehicle token ID: %s", tokenStr),
			Code:        http.StatusBadRequest}
	}
	return tokenID, nil
}

func getDevLicense(c *fiber.Ctx) (common.Address, error) {
	dl, ok := c.Locals("developer_license_address").([]byte)
	if !ok {
		return common.Address{}, richerrors.Error{
			ExternalMsg: "Developer license not found in request context",
			Err:         fmt.Errorf("developer license not found in request context"),
			Code:        fiber.StatusInternalServerError,
		}
	}
	return common.BytesToAddress(dl), nil
}

func getWebhookID(c *fiber.Ctx) (string, error) {
	webhookID := c.Params("webhookId")
	if webhookID == "" {
		return "", richerrors.Error{
			ExternalMsg: "Webhook ID path parameter can not be empty",
			Code:        http.StatusBadRequest,
		}
	}
	if uuid.Validate(webhookID) != nil {
		return "", richerrors.Error{
			ExternalMsg: "Invalid webhook id",
			Err:         fmt.Errorf("invalid webhook Id is not a valid uuid '%s'", webhookID),
			Code:        http.StatusBadRequest,
		}
	}
	return webhookID, nil
}

func (v *VehicleSubscriptionController) subscribeMultipleVehiclesToWebhook(c *fiber.Ctx, webhookID string, developerLicense common.Address, tokenIDs []*big.Int) error {
	for _, tokenID := range tokenIDs {
		hasPerm, err := v.tokenExchangeClient.HasVehiclePermissions(c.Context(), tokenID, developerLicense, []string{
			"privilege:GetNonLocationHistory",
			"privilege:GetLocationHistory",
		})
		if err != nil {
			return richerrors.Error{
				ExternalMsg: "Failed to validate permissions for vehicle " + tokenID.String(),
				Err:         err,
				Code:        http.StatusInternalServerError,
			}
		}
		if !hasPerm {
			return richerrors.Error{
				ExternalMsg: "Insufficient vehicle permissions for vehicle " + tokenID.String(),
				Code:        http.StatusForbidden,
			}
		}
	}

	var failedSubscriptions []FailedSubscription

	for _, tokenID := range tokenIDs {
		_, err := v.repo.CreateVehicleSubscription(c.Context(), tokenID, webhookID)
		if err != nil {
			errMsg := "failed to subscribe vehicle"
			if richErr, ok := richerrors.AsRichError(err); ok {
				errMsg = richErr.ExternalMsg
			}
			failedSubscriptions = append(failedSubscriptions, FailedSubscription{
				VehicleTokenID: tokenID,
				Message:        errMsg,
			})
		}
	}

	if len(failedSubscriptions) > 0 {
		return c.JSON(FailedSubscriptionResponse{FailedSubscriptions: failedSubscriptions})
	}
	if err := v.cache.PopulateCache(c.Context()); err != nil {
		zerolog.Ctx(c.UserContext()).Error().Err(err).Msg("Failed to populate cache after subscribing vehicles")
	}
	return c.JSON(GenericResponse{Message: fmt.Sprintf("Subscribed %d vehicles", len(tokenIDs)-len(failedSubscriptions))})
}

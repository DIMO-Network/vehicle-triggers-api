package webhook

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/auth"
	"github.com/ethereum/go-ethereum/common"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type IdentityClient interface {
	GetSharedVehicles(ctx context.Context, developerLicense []byte) ([]cloudevent.ERC721DID, error)
}
type TokenExchangeClient interface {
	HasVehiclePermissions(ctx context.Context, assetDid cloudevent.ERC721DID, developerLicense common.Address, permissions []string) (bool, error)
}

// VehicleSubscriptionController is a controller for creating, and managing vehicle subscriptions.
type VehicleSubscriptionController struct {
	identityClient      IdentityClient
	tokenExchangeClient TokenExchangeClient
	cache               WebhookCache
	repo                Repository
}

// NewVehicleSubscriptionController creates a new VehicleSubscriptionController.
func NewVehicleSubscriptionController(repo Repository, identityClient IdentityClient, tokenExchangeClient TokenExchangeClient, cache WebhookCache) *VehicleSubscriptionController {
	return &VehicleSubscriptionController{
		identityClient:      identityClient,
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
// @Param        assetDID        path      string  true  "Asset DID"
// @Success      201             {object}  GenericResponse  "Vehicle assigned"
// @Failure      400             {object}  map[string]string  "Bad request"
// @Failure      401             {object}  map[string]string  "Unauthorized"
// @Failure      500             {object}  map[string]string  "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks/{webhookId}/subscribe/{assetDID} [post]
func (v *VehicleSubscriptionController) AssignVehicleToWebhook(c *fiber.Ctx) error {
	webhookID, err := getWebhookID(c)
	if err != nil {
		return err
	}
	assetDid, err := getAssetDID(c)
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

	hasPerm, err := v.tokenExchangeClient.HasVehiclePermissions(c.Context(), assetDid, dl, []string{
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

	_, err = v.repo.CreateVehicleSubscription(c.Context(), assetDid, webhookID)
	if err != nil {
		return fmt.Errorf("failed to assign vehicle: %w", err)
	}

	v.cache.ScheduleRefresh(c.Context())
	return c.Status(http.StatusCreated).JSON(GenericResponse{Message: "Vehicle assigned successfully"})
}

// SubscribeVehiclesFromList godoc
// @Summary      Assign multiple vehicles to a webhook from a list
// @Description  Subscribes each assetDID to the webhook.
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

	return v.subscribeMultipleVehiclesToWebhook(c, webhookID, dl, req.AssetDIDs)
}

// UnsubscribeVehiclesFromList godoc
// @Summary      Unsubscribe multiple vehicles from a webhook using a list
// @Description  Unsubscribes each assetDID from the webhook.
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

	for _, assetDid := range req.AssetDIDs {
		_, err := v.repo.DeleteVehicleSubscription(c.Context(), webhookID, assetDid)
		if err != nil {
			errMsg := "failed to unsubscribe asset"
			if richErr, ok := richerrors.AsRichError(err); ok {
				errMsg = richErr.ExternalMsg
			}
			failedUnSubscriptions = append(failedUnSubscriptions, FailedSubscription{
				AssetDid: assetDid,
				Message:  errMsg,
			})
		}
	}

	if len(failedUnSubscriptions) > 0 {
		return c.JSON(FailedSubscriptionResponse{FailedSubscriptions: failedUnSubscriptions})
	}
	v.cache.ScheduleRefresh(c.Context())
	return c.JSON(GenericResponse{Message: fmt.Sprintf("Unsubscribed %d assets", len(req.AssetDIDs)-len(failedUnSubscriptions))})

}

// RemoveVehicleFromWebhook godoc
// @Summary      Unsubscribe a vehicle from a webhook
// @Description  Removes a vehicle's subscription.
// @Tags         Webhooks
// @Produce      json
// @Param        webhookId       path  string  true  "Webhook ID"
// @Param        assetDID  path  string  true  "Asset DID"
// @Success      200             {object}  GenericResponse  "Vehicle removed"
// @Failure      400             {object}  map[string]string  "Bad request"
// @Failure      401             {object}  map[string]string  "Unauthorized"
// @Failure      500             {object}  map[string]string  "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks/{webhookId}/unsubscribe/{assetDID} [delete]
func (v *VehicleSubscriptionController) RemoveVehicleFromWebhook(c *fiber.Ctx) error {
	webhookID, err := getWebhookID(c)
	if err != nil {
		return err
	}
	assetDid, err := getAssetDID(c)
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

	_, err = v.repo.DeleteVehicleSubscription(c.Context(), webhookID, assetDid)
	if err != nil {
		return richerrors.Error{ExternalMsg: "Failed to unsubscribe", Err: err, Code: http.StatusInternalServerError}
	}
	v.cache.ScheduleRefresh(c.Context())
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

	vehicles, err := v.identityClient.GetSharedVehicles(c.Context(), dl.Bytes())
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
	v.cache.ScheduleRefresh(c.Context())
	return c.JSON(GenericResponse{Message: fmt.Sprintf("Unsubscribed %d vehicles", res)})
}

// ListSubscriptions godoc
// @Summary      List subscriptions for a vehicle
// @Description  Retrieves all webhook subscriptions for a given vehicle.
// @Tags         Webhooks
// @Produce      json
// @Param        assetDID path string true "Asset DID"
// @Success      200            {array}   SubscriptionView
// @Failure      401            {object}  map[string]string  "Unauthorized"
// @Failure      500            {object}  map[string]string  "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks/vehicles/{assetDID} [get]
func (v *VehicleSubscriptionController) ListSubscriptions(c *fiber.Ctx) error {
	assetDid, err := getAssetDID(c)
	if err != nil {
		return err
	}
	dl, err := getDevLicense(c)
	if err != nil {
		return err
	}

	subs, err := v.repo.GetVehicleSubscriptionsByVehicleAndDeveloperLicense(c.Context(), assetDid, dl)
	if err != nil {
		return fmt.Errorf("failed to fetch subscriptions: %w", err)
	}

	out := make([]SubscriptionView, 0, len(subs))
	for _, s := range subs {
		desc := ""
		if s.R != nil && s.R.Trigger != nil {
			desc = s.R.Trigger.Description.String
		}
		did, _ := cloudevent.DecodeERC721DID(s.AssetDid)
		out = append(out, SubscriptionView{
			WebhookID:   s.TriggerID,
			AssetDid:    did,
			CreatedAt:   s.CreatedAt,
			Description: desc,
		})
	}
	return c.JSON(out)
}

// ListVehiclesForWebhook godoc
// @Summary      List all vehicles subscribed to a webhook
// @Description  Returns every vehicle currently subscribed.
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

	assetDIDs := make([]string, len(subs))
	for i, s := range subs {
		assetDIDs[i] = s.AssetDid
	}
	return c.JSON(assetDIDs)
}

func (v *VehicleSubscriptionController) subscribeMultipleVehiclesToWebhook(c *fiber.Ctx, webhookID string, developerLicense common.Address, assetDIDs []cloudevent.ERC721DID) error {
	for _, assetDid := range assetDIDs {
		hasPerm, err := v.tokenExchangeClient.HasVehiclePermissions(c.Context(), assetDid, developerLicense, []string{
			"privilege:GetNonLocationHistory",
			"privilege:GetLocationHistory",
		})
		if err != nil {
			return richerrors.Error{
				ExternalMsg: "Failed to validate permissions for asset " + assetDid.String(),
				Err:         err,
				Code:        http.StatusInternalServerError,
			}
		}
		if !hasPerm {
			return richerrors.Error{
				ExternalMsg: "Insufficient permissions for asset " + assetDid.String(),
				Code:        http.StatusForbidden,
			}
		}
	}

	var failedSubscriptions []FailedSubscription

	for _, assetDid := range assetDIDs {
		_, err := v.repo.CreateVehicleSubscription(c.Context(), assetDid, webhookID)
		if err != nil {
			errMsg := "failed to subscribe asset"
			if richErr, ok := richerrors.AsRichError(err); ok {
				errMsg = richErr.ExternalMsg
			}
			failedSubscriptions = append(failedSubscriptions, FailedSubscription{
				AssetDid: assetDid,
				Message:  errMsg,
			})
		}
	}

	if len(failedSubscriptions) > 0 {
		return c.JSON(FailedSubscriptionResponse{FailedSubscriptions: failedSubscriptions})
	}
	v.cache.ScheduleRefresh(c.Context())
	return c.JSON(GenericResponse{Message: fmt.Sprintf("Subscribed %d assets", len(assetDIDs)-len(failedSubscriptions))})
}

func ownerCheck(ctx context.Context, repo Repository, webhookID string, developerLicense common.Address) error {
	if uuid.Validate(webhookID) != nil {
		return richerrors.Error{
			ExternalMsg: "Invalid webhook id",
			Err:         fmt.Errorf("invalid webhook Id is not a valid uuid '%s'", webhookID),
			Code:        http.StatusBadRequest,
		}
	}
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

func getAssetDID(c *fiber.Ctx) (cloudevent.ERC721DID, error) {
	assetDidStr := c.Params("assetDID")
	if assetDidStr == "" {
		return cloudevent.ERC721DID{}, richerrors.Error{
			ExternalMsg: "Asset DID path parameter can not be empty",
			Code:        http.StatusBadRequest,
		}
	}

	assetDidStr, err := url.PathUnescape(assetDidStr)
	if err != nil {
		return cloudevent.ERC721DID{}, richerrors.Error{
			ExternalMsg: fmt.Sprintf("Invalid asset DID format: %s", err),
			Err:         fmt.Errorf("invalid asset DID: %w", err),
			Code:        http.StatusBadRequest}
	}
	assetDid, err := cloudevent.DecodeERC721DID(assetDidStr)
	if err != nil {
		return cloudevent.ERC721DID{}, richerrors.Error{
			ExternalMsg: fmt.Sprintf("Invalid asset DID format: %s", err),
			Err:         fmt.Errorf("invalid asset DID: %w", err),
			Code:        http.StatusBadRequest}
	}
	return assetDid, nil
}

func getDevLicense(c *fiber.Ctx) (common.Address, error) {
	token, err := auth.GetDexJWT(c)
	if err != nil {
		return common.Address{}, err
	}
	return token.EthereumAddress, nil
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

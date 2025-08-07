package webhook

import (
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"math/big"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/clients/identity"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/clients/tokenexchange"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhookcache"
	"github.com/ethereum/go-ethereum/common"
	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog"
)

type VehicleSubscriptionController struct {
	logger              zerolog.Logger
	identityAPI         *identity.Client
	tokenExchangeClient *tokenexchange.Client
	cache               *webhookcache.WebhookCache
	repo                *triggersrepo.Repository
}

func NewVehicleSubscriptionController(repo *triggersrepo.Repository, logger zerolog.Logger, identityAPI *identity.Client, tokenExchangeClient *tokenexchange.Client, cache *webhookcache.WebhookCache) *VehicleSubscriptionController {
	return &VehicleSubscriptionController{
		logger:              logger,
		identityAPI:         identityAPI,
		tokenExchangeClient: tokenExchangeClient,
		cache:               cache,
		repo:                repo,
	}
}

type SubscriptionView struct {
	EventID        string    `json:"event_id"`
	VehicleTokenID string    `json:"vehicle_token_id"`
	CreatedAt      time.Time `json:"created_at"`
	Description    string    `json:"description"`
}

func getDevLicense(c *fiber.Ctx, logger zerolog.Logger) (common.Address, error) {
	dl, ok := c.Locals("developer_license_address").([]byte)
	if !ok {
		logger.Error().Msg("Developer license not found in request context")
		return common.Address{}, fmt.Errorf("unauthorized")
	}
	return common.BytesToAddress(dl), nil
}

// AssignVehicleToWebhook godoc
// @Summary      Assign a vehicle to a webhook
// @Description  Associates a vehicle with a specific event webhook.
// @Tags         Webhooks
// @Accept       json
// @Produce      json
// @Param        webhookId       path      string  true  "Webhook ID"
// @Param        vehicleTokenId  path      string  true  "Vehicle Token ID"
// @Success      201             {object}  map[string]string  "Vehicle assigned"
// @Failure      400             {object}  map[string]string  "Bad request"
// @Failure      401             {object}  map[string]string  "Unauthorized"
// @Failure      500             {object}  map[string]string  "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks/{webhookId}/subscribe/{vehicleTokenId} [post]
func (v *VehicleSubscriptionController) AssignVehicleToWebhook(c *fiber.Ctx) error {
	webhookID := c.Params("webhookId")
	tokenStr := c.Params("vehicleTokenId")
	if tokenStr == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Vehicle token ID required"})
	}

	tokenID := new(big.Int)
	if _, ok := tokenID.SetString(tokenStr, 10); !ok {
		v.logger.Error().Msg("Invalid vehicle token ID format")
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Invalid token format"})
	}

	dl, err := getDevLicense(c, v.logger)
	if err != nil {
		return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}
	if err := v.OwnerCheck(c, webhookID, dl); err != nil {
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

	if err := v.cache.PopulateCache(c.Context()); err != nil {
		v.logger.Error().Err(err).Msg("cache refresh failed after subscription change")
	}
	return c.Status(http.StatusCreated).JSON(fiber.Map{"message": "Vehicle assigned successfully"})
}

// SubscribeVehiclesFromCSV godoc
// @Summary      Assign multiple vehicles to a webhook from CSV
// @Description  Parses a CSV file from the request body and subscribes each vehicleTokenId to the webhook.
// @Tags         Webhooks
// @Accept       text/csv
// @Produce      json
// @Param        webhookId  path  string  true  "Webhook ID"
// @Success      201        {object}  map[string]string  "Count of subscribed vehicles"
// @Failure      400        {object}  map[string]string  "Bad request"
// @Failure      401        {object}  map[string]string  "Unauthorized"
// @Failure      500        {object}  map[string]string  "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks/{webhookId}/subscribe/csv [post]
func (v *VehicleSubscriptionController) SubscribeVehiclesFromCSV(c *fiber.Ctx) error {
	webhookID := c.Params("webhookId")
	dl, err := getDevLicense(c, v.logger)
	if err != nil {
		return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	owner, err := v.repo.GetWebhookOwner(c.Context(), webhookID)
	if err != nil {
		return richerrors.Error{
			ExternalMsg: "Failed to fetch webhook",
			Err:         err,
			Code:        fiber.StatusInternalServerError,
		}
	}
	if owner != dl {
		return richerrors.Error{
			ExternalMsg: "Webhook not found",
			Err:         fmt.Errorf("developer license %s is not the owner of webhook %s", dl.Hex(), webhookID),
			Code:        fiber.StatusNotFound,
		}
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		v.logger.Error().Err(err).Msg("Failed to get file from form data")
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Missing CSV file"})
	}

	file, err := fileHeader.Open()
	if err != nil {
		v.logger.Error().Err(err).Msg("Failed to open uploaded file")
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to open CSV file"})
	}
	defer func(file multipart.File) {
		err := file.Close()
		if err != nil {
			v.logger.Warn().Err(err).Msg("Failed to close uploaded file")
		}
	}(file)

	reader := csv.NewReader(file)

	records, err := reader.ReadAll()
	if err != nil {
		v.logger.Error().Err(err).Msg("Failed to parse CSV")
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Invalid CSV format"})
	}

	if len(records) == 0 || len(records[0]) == 0 || records[0][0] != "tokenId" {
		v.logger.Error().Msg("CSV header missing or invalid")
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "CSV header must contain 'tokenId'"})
	}

	var vehicleTokenIDs []*big.Int
	for _, record := range records[1:] {
		if len(record) == 0 {
			continue
		}
		tokenStr := record[0]
		tokenID := new(big.Int)
		if _, ok := tokenID.SetString(tokenStr, 10); !ok {
			v.logger.Error().Msgf("Invalid token format in CSV: %v", tokenStr)
			continue
		}
		vehicleTokenIDs = append(vehicleTokenIDs, tokenID)
	}

	// TODO(kevin): should this be from a list  not a csv?
	// if err := v.repo.BulkCreateVehicleSubscriptions(c.Context(), vehicleTokenIDs, webhookID); err != nil {
	// 	v.logger.Error().Err(err).Msg("Failed to bulk create vehicle subscriptions")
	// 	return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to subscribe vehicles"})
	// }

	return c.Status(http.StatusCreated).JSON(fiber.Map{"message": fmt.Sprintf("Subscribed %d vehicles", len(vehicleTokenIDs))})
}

// UnsubscribeVehiclesFromCSV godoc
// @Summary      Unsubscribe multiple vehicles from a webhook using CSV
// @Description  Parses a CSV file from the request body and unsubscribes each vehicleTokenId from the webhook.
// @Tags         Webhooks
// @Accept       text/csv
// @Produce      json
// @Param        webhookId  path  string  true  "Webhook ID"
// @Success      200        {object}  map[string]string  "Count of unsubscribed vehicles"
// @Failure      400        {object}  map[string]string  "Bad request"
// @Failure      401        {object}  map[string]string  "Unauthorized"
// @Failure      500        {object}  map[string]string  "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks/{webhookId}/unsubscribe/csv [delete]
func (v *VehicleSubscriptionController) UnsubscribeVehiclesFromCSV(c *fiber.Ctx) error {
	// webhookID := c.Params("webhookId")
	dl, err := getDevLicense(c, v.logger)
	if err != nil {
		return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}
	_ = dl // TODO(kevin): verify that the developer license is the owner of the webhook

	fileHeader, err := c.FormFile("file")
	if err != nil {
		v.logger.Error().Err(err).Msg("Failed to get file from form data")
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Missing CSV file"})
	}

	file, err := fileHeader.Open()
	if err != nil {
		v.logger.Error().Err(err).Msg("Failed to open uploaded file")
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to open CSV file"})
	}
	defer func(file multipart.File) {
		err := file.Close()
		if err != nil {
			v.logger.Warn().Err(err).Msg("Failed to close uploaded file")
		}
	}(file)

	reader := csv.NewReader(file)

	records, err := reader.ReadAll()
	if err != nil {
		v.logger.Error().Err(err).Msg("Failed to parse CSV")
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Invalid CSV format"})
	}

	if len(records) == 0 || len(records[0]) == 0 || records[0][0] != "tokenId" {
		v.logger.Error().Msg("CSV header missing or invalid")
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "CSV header must contain 'tokenId'"})
	}

	var vehicleTokenIDs []*big.Int
	for _, record := range records[1:] {
		if len(record) == 0 {
			continue
		}
		tokenStr := record[0]
		tokenID := new(big.Int)
		if _, ok := tokenID.SetString(tokenStr, 10); !ok {
			v.logger.Error().Msgf("Invalid token format in CSV: %v", tokenStr)
			continue
		}
		vehicleTokenIDs = append(vehicleTokenIDs, tokenID)
	}

	// TODO(kevin): should this be from a list  not a csv?

	// count, err := v.repo.BulkDeleteVehicleSubscriptions(c.Context(), vehicleTokenIDs, webhookID)
	// if err != nil {
	// 	v.logger.Error().Err(err).Msg("Failed to bulk delete vehicle subscriptions")
	// 	return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to unsubscribe vehicles"})
	// }

	return c.JSON(fiber.Map{"message": fmt.Sprintf("Unsubscribed %d vehicles", len(vehicleTokenIDs))})
}

// RemoveVehicleFromWebhook godoc
// @Summary      Unsubscribe a vehicle from a webhook
// @Description  Removes a vehicle's subscription.
// @Tags         Webhooks
// @Produce      json
// @Param        webhookId       path  string  true  "Webhook ID"
// @Param        vehicleTokenId  path  string  true  "Vehicle Token ID"
// @Success      200             {object}  map[string]string  "Vehicle removed"
// @Failure      400             {object}  map[string]string  "Bad request"
// @Failure      401             {object}  map[string]string  "Unauthorized"
// @Failure      500             {object}  map[string]string  "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks/{webhookId}/unsubscribe/{vehicleTokenId} [delete]
func (v *VehicleSubscriptionController) RemoveVehicleFromWebhook(c *fiber.Ctx) error {
	webhookID := c.Params("webhookId")
	tokenStr := c.Params("vehicleTokenId")
	tokenID := new(big.Int)
	if _, ok := tokenID.SetString(tokenStr, 10); !ok {
		v.logger.Error().Msg("Invalid vehicle token ID")
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Invalid token format"})
	}
	dl, err := getDevLicense(c, v.logger)
	if err != nil {
		return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}
	_ = dl // TODO(kevin): verify that the developer license is the owner of the webhook

	_, err = v.repo.DeleteVehicleSubscription(c.Context(), webhookID, tokenID)
	if err != nil {
		v.logger.Error().Err(err).Msg("Failed to remove subscription")
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to unsubscribe"})
	}
	return c.JSON(fiber.Map{"message": "Vehicle unsubscribed successfully"})
}

// SubscribeAllVehiclesToWebhook godoc
// @Summary      Subscribe all shared vehicles
// @Description  Subscribes every vehicle shared with this developer to the webhook.
// @Tags         Webhooks
// @Produce      json
// @Param        webhookId  path  string  true  "Webhook ID"
// @Success      201        {object}  map[string]string  "Count of subscribed vehicles"
// @Failure      400        {object}  map[string]string  "Bad request"
// @Failure      401        {object}  map[string]string  "Unauthorized"
// @Failure      500        {object}  map[string]string  "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks/{webhookId}/subscribe/all [post]
func (v *VehicleSubscriptionController) SubscribeAllVehiclesToWebhook(c *fiber.Ctx) error {
	webhookID := c.Params("webhookId")
	dl, err := getDevLicense(c, v.logger)
	if err != nil {
		return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}
	_ = dl // TODO(kevin): verify that the developer license is the owner of the webhook

	vehicles, err := v.identityAPI.GetSharedVehicles(c.Context(), dl.Bytes())
	if err != nil {
		v.logger.Error().Err(err).Msg("Failed to fetch shared vehicles")
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	var vehicleTokenIDs []*big.Int
	for _, veh := range vehicles {
		hasPerm, err := v.tokenExchangeClient.HasVehiclePermissions(c.Context(), veh.TokenID, dl, []string{
			"privilege:GetNonLocationHistory",
			"privilege:GetLocationHistory",
		})
		if err != nil {
			v.logger.Error().Err(err).Msg("permission validation failed")
			return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to validate permissions for " + veh.TokenID.String()})
		}
		if !hasPerm {
			return c.Status(http.StatusForbidden).JSON(fiber.Map{"error": "Insufficient vehicle permissions for " + veh.TokenID.String()})
		}
		vehicleTokenIDs = append(vehicleTokenIDs, veh.TokenID)
	}

	// TODO(kevin): handle partial failures
	for _, tokenID := range vehicleTokenIDs {
		_, err := v.repo.CreateVehicleSubscription(c.Context(), tokenID, webhookID)
		if err != nil {
			v.logger.Error().Err(err).Msg("Failed to create vehicle subscription")
			return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to subscribe vehicles"})
		}
	}

	if err := v.cache.PopulateCache(c.Context()); err != nil {
		v.logger.Error().Err(err).Msg("cache refresh failed after subscription change")
	}

	return c.Status(http.StatusCreated).JSON(fiber.Map{"message": fmt.Sprintf("Subscribed %d vehicles", len(vehicleTokenIDs))})
}

// UnsubscribeAllVehiclesFromWebhook godoc
// @Summary      Unsubscribe all shared vehicles
// @Description  Removes every shared vehicle subscription for this webhook.
// @Tags         Webhooks
// @Produce      json
// @Param        webhookId  path  string  true  "Webhook ID"
// @Success      200        {object}  map[string]string  "Count of unsubscribed vehicles"
// @Failure      400        {object}  map[string]string  "Bad request"
// @Failure      401        {object}  map[string]string  "Unauthorized"
// @Failure      500        {object}  map[string]string  "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks/{webhookId}/unsubscribe/all [delete]
func (v *VehicleSubscriptionController) UnsubscribeAllVehiclesFromWebhook(c *fiber.Ctx) error {
	webhookID := c.Params("webhookId")
	dl, err := getDevLicense(c, v.logger)
	if err != nil {
		return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}
	if err := v.OwnerCheck(c, webhookID, dl); err != nil {
		return err
	}

	res, err := v.repo.DeleteAllVehicleSubscriptionsForTrigger(c.Context(), webhookID)
	if err != nil {
		return fmt.Errorf("failed to unsubscribe all vehicles: %w", err)
	}
	return c.JSON(fiber.Map{"message": fmt.Sprintf("Unsubscribed %d vehicles", res)})
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
	tokenStr := c.Params("vehicleTokenId")
	tokenID := new(big.Int)
	if _, ok := tokenID.SetString(tokenStr, 10); !ok {
		v.logger.Error().Msg("Invalid token format")
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Invalid token"})
	}
	dl, err := getDevLicense(c, v.logger)
	if err != nil {
		return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}
	_ = dl // TODO(kevin): verify that the developer license is the owner of the webhook

	// TODO(kevin): get a list of webhooks created by a developer license where the vehicle token id is also subscribed

	subs, err := v.repo.GetVehicleSubscriptionsByVehicleAndDeveloperLicense(c.Context(), tokenID, dl)
	if err != nil {
		v.logger.Error().Err(err).Msg("Failed to fetch subscriptions")
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch"})
	}

	out := make([]SubscriptionView, 0, len(subs))
	for _, s := range subs {
		desc := ""
		if s.R != nil && s.R.Trigger != nil {
			desc = s.R.Trigger.Description.String
		}
		out = append(out, SubscriptionView{
			EventID:        s.TriggerID,
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
	webhookID := c.Params("webhookId")
	dl, err := getDevLicense(c, v.logger)
	if err != nil {
		return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	owner, err := v.repo.GetWebhookOwner(c.Context(), webhookID)
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

	if owner != dl {
		return richerrors.Error{
			ExternalMsg: "Webhook not found",
			Err:         fmt.Errorf("developer license %s is not the owner of webhook %s", dl.Hex(), webhookID),
			Code:        fiber.StatusNotFound,
		}
	}

	subs, err := v.repo.GetVehicleSubscriptionsByTriggerID(c.Context(), webhookID)
	if err != nil {
		return err
	}

	ids := make([]string, len(subs))
	for i, s := range subs {
		ids[i] = s.VehicleTokenID.String()
	}
	return c.JSON(ids)
}

func (v *VehicleSubscriptionController) OwnerCheck(c *fiber.Ctx, webhookID string, developerLicense common.Address) error {
	owner, err := v.repo.GetWebhookOwner(c.Context(), webhookID)
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

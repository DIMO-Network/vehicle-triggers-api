package controllers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/DIMO-Network/shared/db"
	"github.com/DIMO-Network/vehicle-events-api/internal/db/models"
	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
	"github.com/volatiletech/sqlboiler/v4/boil"
	"github.com/volatiletech/sqlboiler/v4/queries/qm"
	"github.com/volatiletech/sqlboiler/v4/types"
)

type VehicleSubscriptionController struct {
	store  db.Store
	logger zerolog.Logger
}

func NewVehicleSubscriptionController(store db.Store, logger zerolog.Logger) *VehicleSubscriptionController {
	return &VehicleSubscriptionController{store: store, logger: logger}
}

type SubscriptionView struct {
	EventID        string    `json:"event_id"`
	VehicleTokenID string    `json:"vehicle_token_id"`
	CreatedAt      time.Time `json:"created_at"`
	Description    string    `json:"description"`
}

func getDevLicense(c *fiber.Ctx, logger zerolog.Logger) ([]byte, error) {
	dl, ok := c.Locals("developer_license_address").([]byte)
	if !ok {
		logger.Error().Msg("Developer license not found in request context")
		return nil, fmt.Errorf("unauthorized")
	}
	return dl, nil
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
	dec := types.Decimal{}
	if err := dec.Scan(tokenStr); err != nil {
		v.logger.Error().Err(err).Msg("Invalid vehicle token ID format")
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Invalid token format"})
	}
	dl, err := getDevLicense(c, v.logger)
	if err != nil {
		return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	ev := &models.EventVehicle{
		VehicleTokenID:          dec,
		EventID:                 webhookID,
		DeveloperLicenseAddress: dl,
		CreatedAt:               time.Now(),
		UpdatedAt:               time.Now(),
	}
	if err := ev.Insert(c.Context(), v.store.DBS().Writer, boil.Infer()); err != nil {
		v.logger.Error().Err(err).Msg("Failed to assign vehicle")
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to assign vehicle"})
	}
	return c.Status(http.StatusCreated).JSON(fiber.Map{"message": "Vehicle assigned successfully"})
}

// RemoveVehicleFromWebhook godoc
// @Summary      Unsubscribe a vehicle from a webhook
// @Description  Removes a vehicle’s subscription.
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
	dec, err := decimal.NewFromString(tokenStr)
	if err != nil {
		v.logger.Error().Err(err).Msg("Invalid vehicle token ID")
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Invalid token format"})
	}
	dl, err := getDevLicense(c, v.logger)
	if err != nil {
		return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	if _, err := models.EventVehicles(
		qm.Where("event_id = ? AND vehicle_token_id = ? AND developer_license_address = ?", webhookID, dec, dl),
	).DeleteAll(c.Context(), v.store.DBS().Writer); err != nil {
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

	vehicles, err := GetSharedVehicles(dl, v.logger)
	if err != nil {
		v.logger.Error().Err(err).Msg("Failed to fetch shared vehicles")
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch vehicles"})
	}

	var count int
	for _, veh := range vehicles {
		dec := types.Decimal{}
		if err := dec.Scan(veh.TokenID); err != nil {
			v.logger.Error().Err(err).Msgf("Invalid token for %v", veh.TokenID)
			continue
		}
		ev := &models.EventVehicle{
			VehicleTokenID:          dec,
			EventID:                 webhookID,
			DeveloperLicenseAddress: dl,
			CreatedAt:               time.Now(),
			UpdatedAt:               time.Now(),
		}
		if err := ev.Insert(c.Context(), v.store.DBS().Writer, boil.Infer()); err != nil {
			v.logger.Error().Err(err).Msgf("Failed to subscribe %v", veh.TokenID)
			continue
		}
		count++
	}
	return c.Status(http.StatusCreated).JSON(fiber.Map{"message": fmt.Sprintf("Subscribed %d vehicles", count)})
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

	res, err := models.EventVehicles(
		qm.Where("event_id = ? AND developer_license_address = ?", webhookID, dl),
	).DeleteAll(c.Context(), v.store.DBS().Writer)
	if err != nil {
		v.logger.Error().Err(err).Msg("Failed to unsubscribe all vehicles")
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to unsubscribe all"})
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
	dec := types.Decimal{}
	if err := dec.Scan(tokenStr); err != nil {
		v.logger.Error().Err(err).Msg("Invalid token format")
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Invalid token"})
	}
	dl, err := getDevLicense(c, v.logger)
	if err != nil {
		return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	subs, err := models.EventVehicles(
		qm.Where("vehicle_token_id = ? AND developer_license_address = ?", dec, dl),
		qm.Load(models.EventVehicleRels.Event),
	).All(c.Context(), v.store.DBS().Reader)
	if err != nil {
		v.logger.Error().Err(err).Msg("Failed to fetch subscriptions")
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch"})
	}

	out := make([]SubscriptionView, 0, len(subs))
	for _, s := range subs {
		desc := ""
		if s.R != nil && s.R.Event != nil {
			desc = s.R.Event.Description.String
		}
		out = append(out, SubscriptionView{
			EventID:        s.EventID,
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

	subs, err := models.EventVehicles(
		qm.Where("event_id = ? AND developer_license_address = ?", webhookID, dl),
	).All(c.Context(), v.store.DBS().Reader)
	if err != nil {
		v.logger.Error().Err(err).Msg("Failed to fetch subscribers")
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch"})
	}

	ids := make([]string, len(subs))
	for i, s := range subs {
		ids[i] = s.VehicleTokenID.String()
	}
	return c.JSON(ids)
}

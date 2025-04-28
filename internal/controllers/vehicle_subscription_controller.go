package controllers

import (
	"fmt"
	"github.com/DIMO-Network/shared/db"
	"github.com/DIMO-Network/vehicle-events-api/internal/db/models"
	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
	"github.com/volatiletech/sqlboiler/v4/boil"
	"github.com/volatiletech/sqlboiler/v4/queries/qm"
	"github.com/volatiletech/sqlboiler/v4/types"
	"net/http"
	"strings"
	"time"
)

type VehicleSubscriptionController struct {
	store  db.Store
	logger zerolog.Logger
}

func NewVehicleSubscriptionController(store db.Store, logger zerolog.Logger) *VehicleSubscriptionController {
	return &VehicleSubscriptionController{
		store:  store,
		logger: logger,
	}
}

type SubscriptionView struct {
	EventID        string    `json:"event_id"`
	VehicleTokenID string    `json:"vehicle_token_id"`
	CreatedAt      time.Time `json:"created_at"`
	Description    string    `json:"description"`
}

func getDevLicense(c *fiber.Ctx, logger zerolog.Logger) ([]byte, error) {
	devLicense, ok := c.Locals("developer_license_address").([]byte)
	if !ok {
		logger.Error().Msg("Developer license not found in request context")
		return nil, fmt.Errorf("unauthorized")
	}
	return devLicense, nil
}

// AssignVehicleToWebhook godoc
// @Summary      Assign a vehicle to a webhook
// @Description  Associates a vehicle with a specific event webhook, optionally using conditions.
// @Tags         Vehicle Subscriptions
// @Accept       json
// @Produce      json
// @Param        vehicleTokenID path string true "Vehicle Token ID"
// @Param        eventID path string true "Event ID"
// @Param        request body object true "Request payload"
// @Success      201 "Vehicle assigned to webhook successfully"
// @Failure      400 "Invalid request payload or vehicle token ID"
// @Failure      401 "Unauthorized"
// @Failure      500 "Internal server error"
// @Security     BearerAuth
// @Router       /subscriptions/{vehicleTokenID}/event/{eventID} [post]
func (v *VehicleSubscriptionController) AssignVehicleToWebhook(c *fiber.Ctx) error {
	vehicleTokenIDStr := c.Params("vehicleTokenID")
	eventID := c.Params("eventID")

	if vehicleTokenIDStr == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Vehicle token ID is required"})
	}

	vehicleTokenIDDecimal := types.Decimal{}
	if err := vehicleTokenIDDecimal.Scan(vehicleTokenIDStr); err != nil {
		v.logger.Error().Err(err).Msg("Invalid vehicle token ID format")
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Invalid vehicle token ID format"})
	}

	var payload struct {
	}
	if err := c.BodyParser(&payload); err != nil {
		v.logger.Error().Err(err).Msg("Invalid request payload")
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request payload"})
	}

	devLicense, err := getDevLicense(c, v.logger)
	if err != nil {
		return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	eventVehicle := &models.EventVehicle{
		VehicleTokenID:          vehicleTokenIDDecimal,
		EventID:                 eventID,
		DeveloperLicenseAddress: devLicense,
		CreatedAt:               time.Now(),
		UpdatedAt:               time.Now(),
	}

	if err := eventVehicle.Insert(c.Context(), v.store.DBS().Writer, boil.Infer()); err != nil {
		v.logger.Error().Err(err).Msg("Failed to assign vehicle to webhook")
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to assign vehicle to webhook"})
	}

	return c.Status(http.StatusCreated).JSON(fiber.Map{"message": "Vehicle assigned to webhook successfully"})
}

// RemoveVehicleFromWebhook godoc
// @Summary      Remove a vehicle from a webhook
// @Description  Unlinks a vehicle from a specific event webhook.
// @Tags         Vehicle Subscriptions
// @Produce      json
// @Param        vehicleTokenID path string true "Vehicle Token ID"
// @Param        eventID path string true "Event ID"
// @Success      200 "Vehicle removed from webhook successfully"
// @Failure      400 "Invalid vehicle token ID"
// @Failure      401 "Unauthorized"
// @Failure      500 "Internal server error"
// @Security     BearerAuth
// @Router       /subscriptions/{vehicleTokenID}/event/{eventID} [delete]
func (v *VehicleSubscriptionController) RemoveVehicleFromWebhook(c *fiber.Ctx) error {
	vehicleTokenIDStr := c.Params("vehicleTokenID")
	eventID := c.Params("eventID")

	vehicleTokenID, err := decimal.NewFromString(vehicleTokenIDStr)
	if err != nil {
		v.logger.Error().Err(err).Msg("Invalid vehicle token ID")
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Invalid vehicle token ID"})
	}

	devLicense, err := getDevLicense(c, v.logger)
	if err != nil {
		return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	_, err = models.EventVehicles(
		qm.Where("vehicle_token_id = ? AND event_id = ? AND developer_license_address = ?", vehicleTokenID, eventID, devLicense),
	).DeleteAll(c.Context(), v.store.DBS().Writer)
	if err != nil {
		v.logger.Error().Err(err).Msg("Failed to remove vehicle from webhook")
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to remove vehicle from webhook"})
	}

	return c.Status(http.StatusOK).JSON(fiber.Map{"message": "Vehicle removed from webhook successfully"})
}

// ListSubscriptions godoc
// @Summary      List subscriptions for a vehicle
// @Description  Retrieves all webhook subscriptions for a given vehicle.
// @Tags         Vehicle Subscriptions
// @Produce      json
// @Param        vehicleTokenID path string true "Vehicle Token ID"
// @Success      200  {array}  object  "List of subscriptions"
// @Failure      401  "Unauthorized"
// @Failure      500  "Internal server error"
// @Security     BearerAuth
// @Router       /subscriptions/{vehicleTokenID} [get]
func (v *VehicleSubscriptionController) ListSubscriptions(c *fiber.Ctx) error {
	// Extract the vehicle token ID from the path
	vehicleTokenIDStr := c.Params("vehicleTokenID")
	if vehicleTokenIDStr == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Vehicle token ID is required"})
	}

	vehicleTokenID := types.Decimal{}
	if err := vehicleTokenID.Scan(vehicleTokenIDStr); err != nil {
		v.logger.Error().Err(err).Msg("Invalid vehicle token ID format")
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Invalid vehicle token ID format"})
	}

	devLicense, err := getDevLicense(c, v.logger)
	if err != nil {
		return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	subscriptions, err := models.EventVehicles(
		qm.Where("vehicle_token_id = ? AND developer_license_address = ?", vehicleTokenID, devLicense),
		qm.Load(models.EventVehicleRels.Event), //eager load
	).All(c.Context(), v.store.DBS().Reader)
	if err != nil {
		v.logger.Error().Err(err).Msg("Failed to retrieve subscriptions")
		return c.Status(fiber.StatusOK).JSON([]models.EventVehicle{})
	}

	resp := make([]SubscriptionView, 0, len(subscriptions))
	for _, sub := range subscriptions {
		var desc string
		if sub.R != nil && sub.R.Event != nil {
			desc = sub.R.Event.Description.String
		}

		view := SubscriptionView{
			EventID:        sub.EventID,
			VehicleTokenID: sub.VehicleTokenID.String(),
			CreatedAt:      sub.CreatedAt,
			Description:    desc,
		}

		resp = append(resp, view)
	}

	return c.JSON(resp)
}

// SubscribeAllVehiclesToWebhook godoc
// @Summary      Subscribe all shared vehicles to a webhook
// @Description  Subscribes every vehicle that has been shared with the authenticated developer license to the specified webhook event.
// @Tags         Vehicle Subscriptions
// @Accept       json
// @Produce      json
// @Param        eventID   path      string  true  "Event ID"
// @Success      201       {object}  map[string]string  "Successfully subscribed count"
// @Failure      400       {object}  map[string]string  "Bad request (e.g. missing eventID)"
// @Failure      401       {object}  map[string]string  "Unauthorized (invalid or missing JWT)"
// @Failure      500       {object}  map[string]string  "Internal server error"
// @Security     BearerAuth
// @Router       /subscriptions/all/event/{eventID} [post]
func (v *VehicleSubscriptionController) SubscribeAllVehiclesToWebhook(c *fiber.Ctx) error {
	eventID := c.Params("eventID")
	if eventID == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Event ID is required"})
	}

	devLicense, err := getDevLicense(c, v.logger)
	if err != nil {
		return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	vehicles, err := GetSharedVehicles(devLicense, v.logger)
	if err != nil {
		v.logger.Error().Err(err).Msg("Failed to retrieve shared vehicles")
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to retrieve shared vehicles"})
	}

	var successCount int
	for _, veh := range vehicles {
		tokenDecimal := types.Decimal{}
		if err := tokenDecimal.Scan(veh.TokenID); err != nil {
			v.logger.Error().Err(err).Msgf("Invalid tokenId format for vehicle %v", veh.TokenID)
			continue
		}

		eventVehicle := &models.EventVehicle{
			VehicleTokenID:          tokenDecimal,
			EventID:                 eventID,
			DeveloperLicenseAddress: devLicense,
			CreatedAt:               time.Now(),
			UpdatedAt:               time.Now(),
		}
		if err := eventVehicle.Insert(c.Context(), v.store.DBS().Writer, boil.Infer()); err != nil {
			v.logger.Error().Err(err).Msgf("Failed to subscribe vehicle %v to webhook", veh.TokenID)
			continue
		}
		successCount++
	}

	return c.Status(http.StatusCreated).JSON(fiber.Map{
		"message": fmt.Sprintf("Successfully subscribed %d vehicles to the webhook", successCount),
	})
}

// UnsubscribeAllVehiclesFromWebhook godoc
// @Summary      Unsubscribe all shared vehicles from a webhook
// @Description  Removes subscription for every vehicle shared with the developer from the specified event.
// @Tags         Vehicle Subscriptions
// @Produce      json
// @Param        eventID  path   string true "Event ID"
// @Success      200      {object} map[string]string
// @Failure      400      {object} map[string]string
// @Failure      401      {object} map[string]string
// @Failure      500      {object} map[string]string
// @Security     BearerAuth
// @Router       /subscriptions/event/{eventID}/subscribe/all [delete]
func (v *VehicleSubscriptionController) UnsubscribeAllVehiclesFromWebhook(c *fiber.Ctx) error {
	eventID := c.Params("eventID")
	if eventID == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Event ID is required"})
	}
	devLicense, err := getDevLicense(c, v.logger)
	if err != nil {
		return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	// Bulk delete in one query:
	_, err = models.EventVehicles(
		qm.Where("event_id = ? AND developer_license_address = ?", eventID, devLicense),
	).DeleteAll(c.Context(), v.store.DBS().Writer)
	if err != nil {
		v.logger.Error().Err(err).Msg("Failed to unsubscribe all vehicles")
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to unsubscribe vehicles"})
	}
	return c.Status(http.StatusOK).JSON(fiber.Map{"message": "All vehicles unsubscribed successfully"})
}

// SubscribeMultipleVehiclesToWebhook subscribes one or more vehicles to a given webhook
// @Summary      Subscribe multiple vehicles to a webhook
// @Description  Takes a JSON array of vehicleTokenIDs in the request body and subscribes each to the event.
// @Tags         Vehicle Subscriptions
// @Accept       json
// @Produce      json
// @Param        eventID           path      string   true  "Event ID"
// @Param        vehicleTokenIDs   body      []string true  "List of Vehicle Token IDs"
// @Success      201               {object}  map[string]string  "Summary message"
// @Failure      400               {object}  map[string]string  "Invalid request payload or missing IDs"
// @Failure      401               {object}  map[string]string  "Unauthorized"
// @Failure      500               {object}  map[string]string  "Internal server error"
// @Security     BearerAuth
// @Router       /subscriptions/{eventID} [post]
func (v *VehicleSubscriptionController) SubscribeMultipleVehiclesToWebhook(c *fiber.Ctx) error {
	eventID := c.Params("eventID")
	if eventID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Event ID is required"})
	}
	var req struct {
		VehicleTokenIDs []string `json:"vehicleTokenIDs"`
	}
	if err := c.BodyParser(&req); err != nil {
		v.logger.Error().Err(err).Msg("Invalid request payload for SubscribeMultipleVehiclesToWebhook")
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request payload"})
	}
	if len(req.VehicleTokenIDs) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "vehicleTokenIDs field is required"})
	}

	devLicense, err := getDevLicense(c, v.logger)
	if err != nil {
		return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	var successCount int
	for _, idStr := range req.VehicleTokenIDs {
		trimmed := strings.TrimSpace(idStr)
		tokenDecimal := types.Decimal{}
		if err := tokenDecimal.Scan(trimmed); err != nil {
			v.logger.Error().
				Err(err).
				Str("vehicleTokenID", trimmed).
				Msg("Invalid vehicleTokenID format")
			continue
		}

		ev := &models.EventVehicle{
			VehicleTokenID:          tokenDecimal,
			EventID:                 eventID,
			DeveloperLicenseAddress: devLicense,
			CreatedAt:               time.Now(),
			UpdatedAt:               time.Now(),
		}
		if err := ev.Insert(c.Context(), v.store.DBS().Writer, boil.Infer()); err != nil {
			v.logger.Error().
				Err(err).
				Str("vehicleTokenID", trimmed).
				Msg("Failed to subscribe vehicle to webhook")
			continue
		}
		successCount++
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"message": fmt.Sprintf("Successfully subscribed %d vehicles to the webhook", successCount),
	})
}

// UnsubscribeMultipleVehiclesFromWebhook godoc
// @Summary      Unsubscribe multiple vehicles from a webhook
// @Description  Takes a JSON array of vehicleTokenIDs and unsubscribes each from the event.
// @Tags         Vehicle Subscriptions
// @Accept       json
// @Produce      json
// @Param        eventID           path   string   true "Event ID"
// @Param        vehicleTokenIDs   body   []string true "List of Vehicle Token IDs"
// @Success      200               {object} map[string]string
// @Failure      400               {object} map[string]string
// @Failure      401               {object} map[string]string
// @Failure      500               {object} map[string]string
// @Security     BearerAuth
// @Router       /subscriptions/event/{eventID}/subscribe [delete]
func (v *VehicleSubscriptionController) UnsubscribeMultipleVehiclesFromWebhook(c *fiber.Ctx) error {
	eventID := c.Params("eventID")
	if eventID == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Event ID is required"})
	}
	var req struct {
		VehicleTokenIDs []string `json:"vehicleTokenIDs"`
	}
	if err := c.BodyParser(&req); err != nil {
		v.logger.Error().Err(err).Msg("Invalid request payload")
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request payload"})
	}
	if len(req.VehicleTokenIDs) == 0 {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "vehicleTokenIDs field is required"})
	}

	devLicense, err := getDevLicense(c, v.logger)
	if err != nil {
		return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	var successCount int
	for _, idStr := range req.VehicleTokenIDs {
		trimmed := strings.TrimSpace(idStr)
		dec := types.Decimal{}
		if err := dec.Scan(trimmed); err != nil {
			v.logger.Error().Err(err).Str("vehicleTokenID", trimmed).Msg("Invalid token format")
			continue
		}
		_, err := models.EventVehicles(
			qm.Where("event_id = ? AND vehicle_token_id = ? AND developer_license_address = ?", eventID, dec, devLicense),
		).DeleteAll(c.Context(), v.store.DBS().Writer)
		if err != nil {
			v.logger.Error().Err(err).Str("vehicleTokenID", trimmed).Msg("Failed to unsubscribe vehicle")
			continue
		}
		successCount++
	}

	return c.Status(http.StatusOK).JSON(fiber.Map{
		"message": fmt.Sprintf("Successfully unsubscribed %d vehicles", successCount),
	})
}

// ListVehiclesForWebhook godoc
// @Summary      List all vehicles subscribed to a webhook
// @Description  Retrieves all vehicle subscriptions for the specified webhook ID.
// @Tags         Vehicle Subscriptions
// @Produce      json
// @Param        webhookId   path      string  true  "Webhook ID"
// @Success      200         {array}   SubscriptionView
// @Failure      401         {object}  map[string]string  "Unauthorized"
// @Failure      500         {object}  map[string]string  "Internal server error"
// @Security     BearerAuth
// @Router       /v1/webhooks/{webhookId} [get]
func (v *VehicleSubscriptionController) ListVehiclesForWebhook(c *fiber.Ctx) error {
	webhookId := c.Params("webhookId")
	if webhookId == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Webhook ID is required"})
	}
	devLicense, err := getDevLicense(c, v.logger)
	if err != nil {
		return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}
	subs, err := models.EventVehicles(
		qm.Where("event_id = ? AND developer_license_address = ?", webhookId, devLicense),
	).All(c.Context(), v.store.DBS().Reader)
	if err != nil {
		v.logger.Error().Err(err).Msg("Failed to retrieve subscriptions for webhook")
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to list subscriptions"})
	}

	resp := make([]SubscriptionView, len(subs))
	for i, sub := range subs {
		resp[i] = SubscriptionView{
			EventID:        sub.EventID,
			VehicleTokenID: sub.VehicleTokenID.String(),
			CreatedAt:      sub.CreatedAt,
			Description:    sub.R.Event.Description.String,
		}
	}
	return c.JSON(resp)
}

package controllers

import (
	"encoding/json"
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

	// Validate vehicle permissions by calling the Identity API.
	identityURL := fmt.Sprintf("https://api.dimo.org/identity/v1/vehicles/%s/sacds", vehicleTokenIDStr)
	req, err := http.NewRequest("GET", identityURL, nil)
	if err != nil {
		v.logger.Error().Err(err).Msg("Failed to create request to Identity API")
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to validate vehicle permissions"})
	}
	// Forward the developer JWT from the incoming request.
	req.Header.Set("Authorization", c.Get("Authorization"))
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		v.logger.Error().Err(err).Msg("Failed to call Identity API")
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to validate vehicle permissions"})
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		v.logger.Error().Msgf("Identity API returned non-200 status: %d", resp.StatusCode)
		return c.Status(http.StatusForbidden).JSON(fiber.Map{"error": "Vehicle permissions could not be verified"})
	}
	var sacdResponse []struct {
		DeveloperAppID string `json:"developerAppId"`
		Privileges     []int  `json:"privileges"`
		VehicleNodeID  string `json:"vehicleNodeId"`
		GrantedAt      string `json:"grantedAt"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sacdResponse); err != nil {
		v.logger.Error().Err(err).Msg("Failed to decode Identity API response")
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to validate vehicle permissions"})
	}
	requiredPrivileges := []int{1, 3, 4}
	permValid := false
	for _, item := range sacdResponse {
		if containsAllPrivileges(item.Privileges, requiredPrivileges) {
			permValid = true
			break
		}
	}
	if !permValid {
		return c.Status(http.StatusForbidden).JSON(fiber.Map{"error": "Insufficient vehicle permissions. Required privileges: 1,3,4"})
	}

	var payload struct {
	}
	if err := c.BodyParser(&payload); err != nil {
		v.logger.Error().Err(err).Msg("Invalid request payload")
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request payload"})
	}

	devLicense, ok := c.Locals("developer_license_address").([]byte)
	if !ok {
		v.logger.Error().Msg("Developer license not found in request context")
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

// containsAllPrivileges returns true if privileges contain every int in required.
func containsAllPrivileges(privileges, required []int) bool {
	privMap := make(map[int]bool)
	for _, p := range privileges {
		privMap[p] = true
	}
	for _, r := range required {
		if !privMap[r] {
			return false
		}
	}
	return true
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

	devLicense, ok := c.Locals("developer_license_address").([]byte)
	if !ok {
		v.logger.Error().Msg("Developer license not found in request context")
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
	vehicleTokenIDStr := c.Params("vehicleTokenID")
	if vehicleTokenIDStr == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Vehicle token ID is required"})
	}

	vehicleTokenID := types.Decimal{}
	if err := vehicleTokenID.Scan(vehicleTokenIDStr); err != nil {
		v.logger.Error().Err(err).Msg("Invalid vehicle token ID format")
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Invalid vehicle token ID format"})
	}

	devLicense, ok := c.Locals("developer_license_address").([]byte)
	if !ok {
		v.logger.Error().Msg("Developer license not found in request context")
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
// @Summary      Subscribe all vehicles shared with the developer to a webhook
// @Description  Retrieves all vehicles shared with the current developer via the Identity API and subscribes each one to the specified webhook event.
// @Tags         Vehicle Subscriptions
// @Produce      json
// @Param        eventID path string true "Event ID"
// @Param        Authorization header string true "Bearer token"
// @Success      201 {object} map[string]string "Successfully subscribed X vehicles to the webhook"
// @Failure      400 {object} map[string]string "Event ID is required"
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /subscriptions/all/event/{eventID} [post]
func (v *VehicleSubscriptionController) SubscribeAllVehiclesToWebhook(c *fiber.Ctx) error {
	eventID := c.Params("eventID")
	if eventID == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Event ID is required"})
	}
	devLicense, ok := c.Locals("developer_license_address").([]byte)
	if !ok {
		v.logger.Error().Msg("Developer license not found in request context")
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

// SubscribeMultipleVehiclesToWebhook godoc
// @Summary      Subscribe multiple vehicles to a webhook
// @Description  Accepts a comma-separated list of vehicle token IDs and subscribes each one to the specified webhook event.
// @Tags         Vehicle Subscriptions
// @Produce      json
// @Param        eventID path string true "Event ID"
// @Param        vehicleTokenIDs query string true "Comma-separated list of vehicle token IDs"
// @Param        Authorization header string true "Bearer token"
// @Success      201 {object} map[string]string "Successfully subscribed X vehicles to the webhook"
// @Failure      400 {object} map[string]string "Event ID and vehicle token IDs are required"
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /subscriptions/{eventID} [post]
func (v *VehicleSubscriptionController) SubscribeMultipleVehiclesToWebhook(c *fiber.Ctx) error {
	eventID := c.Params("eventID")
	if eventID == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Event ID is required"})
	}
	vehicleIDsParam := c.Query("vehicleTokenIDs")
	if vehicleIDsParam == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Vehicle token IDs are required"})
	}
	ids := strings.Split(vehicleIDsParam, ",")
	devLicense, ok := c.Locals("developer_license_address").([]byte)
	if !ok {
		v.logger.Error().Msg("Developer license not found in request context")
		return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}
	var successCount int
	for _, idStr := range ids {
		idStr = strings.TrimSpace(idStr)
		tokenDecimal := types.Decimal{}
		if err := tokenDecimal.Scan(idStr); err != nil {
			v.logger.Error().Err(err).Msgf("Invalid token ID format for vehicle %v", idStr)
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
			v.logger.Error().Err(err).Msgf("Failed to subscribe vehicle %v to webhook", idStr)
			continue
		}
		successCount++
	}
	return c.Status(http.StatusCreated).JSON(fiber.Map{
		"message": fmt.Sprintf("Successfully subscribed %d vehicles to the webhook", successCount),
	})
}

// UnshareVehiclePermissions godoc
// @Summary      Remove all subscriptions for a vehicle when permissions are revoked
// @Description  Removes all webhook subscriptions for a given vehicle token when the vehicle's permissions are un-shared by the user.
// @Tags         Vehicle Subscriptions
// @Produce      json
// @Param        vehicleTokenID path string true "Vehicle Token ID"
// @Param        Authorization header string true "Bearer token"
// @Success      200 {object} map[string]string "Webhook subscriptions removed successfully"
// @Failure      400 {object} map[string]string "Invalid vehicle token ID"
// @Failure      401 {object} map[string]string "Unauthorized"
// @Failure      500 {object} map[string]string "Internal server error"
// @Router       /subscriptions/{vehicleTokenID}/unshare [delete]
func (v *VehicleSubscriptionController) UnshareVehiclePermissions(c *fiber.Ctx) error {
	vehicleTokenIDStr := c.Params("vehicleTokenID")
	if vehicleTokenIDStr == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Vehicle token ID is required"})
	}

	vehicleTokenID, err := decimal.NewFromString(vehicleTokenIDStr)
	if err != nil {
		v.logger.Error().Err(err).Msg("Invalid vehicle token ID")
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Invalid vehicle token ID"})
	}

	devLicense, ok := c.Locals("developer_license_address").([]byte)
	if !ok {
		v.logger.Error().Msg("Developer license not found in request context")
		return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	_, err = models.EventVehicles(
		qm.Where("vehicle_token_id = ? AND developer_license_address = ?", vehicleTokenID, devLicense),
	).DeleteAll(c.Context(), v.store.DBS().Writer)
	if err != nil {
		v.logger.Error().Err(err).Msg("Failed to remove webhook subscriptions for unshared vehicle")
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to remove webhook subscriptions"})
	}

	return c.Status(http.StatusOK).JSON(fiber.Map{"message": "Webhook subscriptions removed successfully"})
}

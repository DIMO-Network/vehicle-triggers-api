package controllers

import (
	"encoding/json"
	"github.com/DIMO-Network/shared/db"
	"github.com/DIMO-Network/vehicle-events-api/internal/db/models"
	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
	"github.com/volatiletech/null/v8"
	"github.com/volatiletech/sqlboiler/v4/boil"
	"github.com/volatiletech/sqlboiler/v4/queries/qm"
	"github.com/volatiletech/sqlboiler/v4/types"
	"net/http"
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

// AssignVehicleToWebhook assigns a vehicle to a webhook
func (v *VehicleSubscriptionController) AssignVehicleToWebhook(c *fiber.Ctx) error {
	type Condition struct {
		Field    string `json:"field"`
		Operator string `json:"operator"`
		Value    string `json:"value"`
	}
	type RequestPayload struct {
		Conditions []Condition `json:"conditions"` // Optional
	}

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

	var payload RequestPayload
	if err := c.BodyParser(&payload); err != nil {
		v.logger.Error().Err(err).Msg("Invalid request payload")
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request payload"})
	}

	devLicense, ok := c.Locals("developer_license_address").([]byte)
	if !ok {
		v.logger.Error().Msg("Developer license not found in request context")
		return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	existingSubscription, err := models.EventVehicles(
		qm.Where("vehicle_token_id = ? AND event_id = ? AND developer_license_address = ?", vehicleTokenIDDecimal, eventID, devLicense),
	).One(c.Context(), v.store.DBS().Reader)

	if err == nil && existingSubscription != nil {
		return c.Status(http.StatusConflict).JSON(fiber.Map{"error": "You are already subscribed to this event."})
	}

	conditionsJSON := "{}"
	if len(payload.Conditions) > 0 {
		serializedConditions, err := json.Marshal(payload.Conditions)
		if err != nil {
			v.logger.Error().Err(err).Msg("Failed to serialize conditions")
			return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to process conditions"})
		}
		conditionsJSON = string(serializedConditions)
	}

	eventVehicle := &models.EventVehicle{
		VehicleTokenID:          vehicleTokenIDDecimal,
		EventID:                 eventID,
		DeveloperLicenseAddress: devLicense,
		CreatedAt:               time.Now(),
		UpdatedAt:               time.Now(),
		ConditionData:           null.JSONFrom([]byte(conditionsJSON)),
	}

	if err := eventVehicle.Insert(c.Context(), v.store.DBS().Writer, boil.Infer()); err != nil {
		v.logger.Error().Err(err).Msg("Failed to assign vehicle to webhook")
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to assign vehicle to webhook"})
	}

	return c.Status(http.StatusCreated).JSON(fiber.Map{"message": "Vehicle assigned to webhook successfully"})
}

// RemoveVehicleFromWebhook removes a vehicle from a webhook
func (v *VehicleSubscriptionController) RemoveVehicleFromWebhook(c *fiber.Ctx) error {
	// Extract path parameters
	vehicleTokenIDStr := c.Params("vehicleTokenID")
	eventID := c.Params("eventID")

	vehicleTokenID, err := decimal.NewFromString(vehicleTokenIDStr)
	if err != nil {
		v.logger.Error().Err(err).Msg("Invalid vehicle token ID")
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Invalid vehicle token ID"})
	}

	// Hardcoded developer license for now
	devLicense, ok := c.Locals("developer_license_address").([]byte)
	if !ok {
		v.logger.Error().Msg("Developer license not found in request context")
		return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	// Delete the record
	_, err = models.EventVehicles(
		qm.Where("vehicle_token_id = ? AND event_id = ? AND developer_license_address = ?", vehicleTokenID, eventID, devLicense),
	).DeleteAll(c.Context(), v.store.DBS().Writer)
	if err != nil {
		v.logger.Error().Err(err).Msg("Failed to remove vehicle from webhook")
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to remove vehicle from webhook"})
	}

	return c.Status(http.StatusOK).JSON(fiber.Map{"message": "Vehicle removed from webhook successfully"})
}

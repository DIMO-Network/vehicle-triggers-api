package triggersrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"time"

	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/ericlagergren/decimal"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/volatiletech/null/v8"
	"github.com/volatiletech/sqlboiler/v4/boil"
	"github.com/volatiletech/sqlboiler/v4/queries/qm"
	"github.com/volatiletech/sqlboiler/v4/types"
)

const (
	schemaName = "vehicle_events_api"
)

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// CreateTriggerRequest represents the data needed to create a new trigger.
type CreateTriggerRequest struct {
	Service                 string
	MetricName              string
	Condition               string
	TargetURI               string
	Status                  string
	Description             string
	CooldownPeriod          int
	DeveloperLicenseAddress common.Address
}

func (req CreateTriggerRequest) Validate() error {
	// Validate required fields
	if req.MetricName == "" {
		return fmt.Errorf("%w metric_name is required", ValidationError)
	}
	if req.DeveloperLicenseAddress == (common.Address{}) {
		return fmt.Errorf("%w developer_license_address is required", ValidationError)
	}
	if req.Service == "" {
		return fmt.Errorf("%w service is required", ValidationError)
	}
	if req.Condition == "" {
		return fmt.Errorf("%w condition is required", ValidationError)
	}
	if req.TargetURI == "" {
		return fmt.Errorf("%w target_uri is required", ValidationError)
	}
	if req.Status == "" {
		return fmt.Errorf("%w status is required", ValidationError)
	}
	return nil
}

// CreateTrigger creates a new trigger/webhook.
func (r *Repository) CreateTrigger(ctx context.Context, req CreateTriggerRequest) (*models.Trigger, error) {
	if err := req.Validate(); err != nil {
		return nil, richerrors.Error{
			ExternalMsg: "Invalid request: " + err.Error(),
			Err:         err,
			Code:        http.StatusBadRequest,
		}
	}

	trigger := &models.Trigger{
		ID:                      uuid.New().String(),
		Service:                 req.Service,
		MetricName:              req.MetricName,
		Condition:               req.Condition,
		Description:             null.StringFrom(req.Description),
		TargetURI:               req.TargetURI,
		CooldownPeriod:          req.CooldownPeriod,
		DeveloperLicenseAddress: req.DeveloperLicenseAddress.Bytes(),
		Status:                  req.Status,
	}

	if err := trigger.Insert(ctx, r.db, boil.Infer()); err != nil {
		return nil, richerrors.Error{
			ExternalMsg: "Error creating trigger",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}

	return trigger, nil
}

// GetTriggersByDeveloperLicense retrieves all triggers for a developer license.
func (r *Repository) GetTriggersByDeveloperLicense(ctx context.Context, developerLicenseAddress common.Address) ([]*models.Trigger, error) {
	triggers, err := models.Triggers(
		models.TriggerWhere.DeveloperLicenseAddress.EQ(developerLicenseAddress.Bytes()),
		qm.OrderBy("id"),
	).All(ctx, r.db)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, richerrors.Error{
				ExternalMsg: "No triggers found",
				Err:         err,
				Code:        http.StatusNotFound,
			}
		}
		return nil, richerrors.Error{
			ExternalMsg: "Error getting triggers",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}

	if triggers == nil {
		triggers = make([]*models.Trigger, 0)
	}

	return triggers, nil
}

// GetTriggerByIDAndDeveloperLicense retrieves a specific trigger by ID and developer license.
func (r *Repository) GetTriggerByIDAndDeveloperLicense(ctx context.Context, triggerID string, developerLicenseAddress common.Address) (*models.Trigger, error) {
	if triggerID == "" {
		return nil, richerrors.Error{
			ExternalMsg: "Trigger id is required",
			Err:         ValidationError,
			Code:        http.StatusBadRequest,
		}
	}
	if developerLicenseAddress == (common.Address{}) {
		return nil, richerrors.Error{
			ExternalMsg: "Developer license address is required",
			Err:         ValidationError,
			Code:        http.StatusBadRequest,
		}
	}

	trigger, err := models.Triggers(
		models.TriggerWhere.ID.EQ(triggerID),
		models.TriggerWhere.DeveloperLicenseAddress.EQ(developerLicenseAddress.Bytes()),
	).One(ctx, r.db)

	if err != nil {
		return nil, richerrors.Error{
			ExternalMsg: "Error getting trigger",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}

	return trigger, nil
}

// UpdateTrigger updates an existing trigger.
func (r *Repository) UpdateTrigger(ctx context.Context, trigger *models.Trigger) error {
	ret, err := trigger.Update(ctx, r.db, boil.Blacklist(models.TriggerColumns.ID,
		models.TriggerColumns.ID,
		models.TriggerColumns.DeveloperLicenseAddress,
		models.TriggerColumns.Service,
		models.TriggerColumns.CreatedAt,
	))
	if err != nil {
		return richerrors.Error{
			ExternalMsg: "Error updating trigger",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}
	if ret == 0 {
		return richerrors.Error{
			ExternalMsg: "Trigger not found",
			Err:         sql.ErrNoRows,
			Code:        http.StatusNotFound,
		}
	}
	return nil
}

// DeleteTrigger deletes a trigger by ID and developer license
func (r *Repository) DeleteTrigger(ctx context.Context, triggerID string, developerLicenseAddress common.Address) error {
	trigger, err := models.Triggers(
		models.TriggerWhere.ID.EQ(triggerID),
		models.TriggerWhere.DeveloperLicenseAddress.EQ(developerLicenseAddress.Bytes()),
	).One(ctx, r.db)

	if err != nil {
		return richerrors.Error{
			ExternalMsg: "Error deleting trigger",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}

	_, err = trigger.Delete(ctx, r.db)
	if err != nil {
		return richerrors.Error{
			ExternalMsg: "Error deleting trigger",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}
	return nil
}

// Vehicle Subscription operations

// CreateVehicleSubscription creates a new vehicle subscription
func (r *Repository) CreateVehicleSubscription(ctx context.Context, vehicleTokenID *big.Int, triggerID string) (*models.VehicleSubscription, error) {
	if vehicleTokenID.Cmp(big.NewInt(0)) == 0 {
		return nil, richerrors.Error{
			ExternalMsg: "Vehicle token ID is required",
			Err:         ValidationError,
			Code:        http.StatusBadRequest,
		}
	}
	if triggerID == "" {
		return nil, richerrors.Error{
			ExternalMsg: "Trigger id is required",
			Err:         ValidationError,
			Code:        http.StatusBadRequest,
		}
	}

	subscription := &models.VehicleSubscription{
		VehicleTokenID: bigIntToDecimal(vehicleTokenID),
		TriggerID:      triggerID,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	if err := subscription.Insert(ctx, r.db, boil.Infer()); err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) {
			if pqErr.Code == ForeignKeyViolation {
				return nil, richerrors.Error{
					ExternalMsg: "Trigger not found",
					Err:         err,
					Code:        http.StatusNotFound,
				}
			}
		}
		return nil, err
	}

	return subscription, nil
}

// GetVehicleSubscriptionsByTriggerID retrieves all vehicle subscriptions for a trigger
func (r *Repository) GetVehicleSubscriptionsByTriggerID(ctx context.Context, triggerID string) ([]*models.VehicleSubscription, error) {
	subscriptions, err := models.VehicleSubscriptions(
		models.VehicleSubscriptionWhere.TriggerID.EQ(triggerID),
	).All(ctx, r.db)

	if err != nil {
		return nil, richerrors.Error{
			ExternalMsg: "Failed to get vehicle subscriptions",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}

	return subscriptions, nil
}

// GetVehicleSubscriptionsByVehicleAndDeveloperLicense retrieves all subscriptions for webhook IDs that the device_license created.
func (r *Repository) GetVehicleSubscriptionsByVehicleAndDeveloperLicense(ctx context.Context, vehicleTokenID *big.Int, developerLicenseAddress common.Address) ([]*models.VehicleSubscription, error) {
	if developerLicenseAddress == (common.Address{}) {
		return nil, richerrors.Error{
			ExternalMsg: "Developer license address is required",
			Err:         ValidationError,
			Code:        http.StatusBadRequest,
		}
	}
	if vehicleTokenID == nil || vehicleTokenID.Cmp(big.NewInt(0)) == 0 {
		return nil, richerrors.Error{
			ExternalMsg: "Vehicle token ID is required",
			Err:         ValidationError,
			Code:        http.StatusBadRequest,
		}
	}
	dec := bigIntToDecimal(vehicleTokenID)
	subscriptions, err := models.VehicleSubscriptions(
		models.VehicleSubscriptionWhere.VehicleTokenID.EQ(dec),
		qm.InnerJoin(fmt.Sprintf("%s.%s on %s = %s",
			schemaName,
			models.TableNames.Triggers,
			models.TriggerTableColumns.ID,
			models.VehicleSubscriptionTableColumns.TriggerID,
		)),
		qm.Where(fmt.Sprintf("%s = ?",
			models.TriggerTableColumns.DeveloperLicenseAddress,
		), developerLicenseAddress.Bytes()),
	).All(ctx, r.db)

	if err != nil {
		return nil, richerrors.Error{
			ExternalMsg: "Failed to get vehicle subscriptions",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}

	return subscriptions, nil
}

// DeleteVehicleSubscription deletes a specific vehicle subscription.
func (r *Repository) DeleteVehicleSubscription(ctx context.Context, triggerID string, vehicleTokenID *big.Int) (int64, error) {
	if triggerID == "" {
		return 0, richerrors.Error{
			ExternalMsg: "Trigger id is required",
			Err:         ValidationError,
			Code:        http.StatusBadRequest,
		}
	}
	if vehicleTokenID == nil || vehicleTokenID.Cmp(big.NewInt(0)) == 0 {
		return 0, richerrors.Error{
			ExternalMsg: "Vehicle token ID is required",
			Err:         ValidationError,
			Code:        http.StatusBadRequest,
		}
	}
	return models.VehicleSubscriptions(
		models.VehicleSubscriptionWhere.TriggerID.EQ(triggerID),
		models.VehicleSubscriptionWhere.VehicleTokenID.EQ(bigIntToDecimal(vehicleTokenID)),
	).DeleteAll(ctx, r.db)
}

// DeleteAllVehicleSubscriptionsForTrigger deletes all vehicle subscriptions for a trigger.
func (r *Repository) DeleteAllVehicleSubscriptionsForTrigger(ctx context.Context, triggerID string) (int64, error) {
	if triggerID == "" {
		return 0, richerrors.Error{
			ExternalMsg: "Trigger id is required",
			Err:         ValidationError,
			Code:        http.StatusBadRequest,
		}
	}
	return models.VehicleSubscriptions(
		models.VehicleSubscriptionWhere.TriggerID.EQ(triggerID),
	).DeleteAll(ctx, r.db)
}

// GetWebhookOwner returns the developer license address of the webhook owner
func (r *Repository) GetWebhookOwner(ctx context.Context, triggerID string) (common.Address, error) {
	trigger, err := models.Triggers(
		models.TriggerWhere.ID.EQ(triggerID),
	).One(ctx, r.db)

	if err != nil {
		return common.Address{}, err
	}

	return common.BytesToAddress(trigger.DeveloperLicenseAddress), nil
}

func bigIntToDecimal(vehicleTokenID *big.Int) types.Decimal {
	dec := types.NewDecimal(new(decimal.Big))
	dec.SetBigMantScale(vehicleTokenID, 0)
	return dec
}

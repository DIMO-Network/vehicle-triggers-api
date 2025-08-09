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
	// StatusEnabled is the status of a trigger that is enabled.
	StatusEnabled = "enabled"
	// StatusDisabled is the status of a trigger that is disabled.
	StatusDisabled = "disabled"
	// StatusFailed is the status of a trigger that has failed.
	StatusFailed = "failed"
	// StatusDeleted is the status of a trigger that has been deleted.
	StatusDeleted = "deleted"
)

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// CreateTriggerRequest represents the data needed to create a new trigger.
type CreateTriggerRequest struct {
	DisplayName             string
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
		return fmt.Errorf("%w metricName is required", ValidationError)
	}
	if req.DeveloperLicenseAddress == (common.Address{}) {
		return fmt.Errorf("%w developerLicenseAddress is required", ValidationError)
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
	if req.CooldownPeriod < 0 {
		return fmt.Errorf("%w cooldownPeriod cannot be negative", ValidationError)
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
	id := uuid.New().String()
	displayName := req.DisplayName
	if displayName == "" {
		displayName = id
	}

	trigger := &models.Trigger{
		ID:                      id,
		DisplayName:             displayName,
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
		models.TriggerWhere.Status.NEQ(StatusDeleted),
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
		models.TriggerWhere.Status.NEQ(StatusDeleted),
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
	// Find the trigger owned by the developer and not already deleted
	trigger, err := models.Triggers(
		models.TriggerWhere.ID.EQ(triggerID),
		models.TriggerWhere.DeveloperLicenseAddress.EQ(developerLicenseAddress.Bytes()),
		models.TriggerWhere.Status.NEQ(StatusDeleted),
	).One(ctx, r.db)
	if err != nil {
		return richerrors.Error{
			ExternalMsg: "Error deleting trigger",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return richerrors.Error{
			ExternalMsg: "Error deleting trigger",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}
	defer func() {
		// Rollback if still active
		_ = tx.Rollback()
	}()

	// Soft-delete the trigger by setting status to Deleted
	trigger.Status = StatusDeleted
	if _, err := trigger.Update(ctx, tx, boil.Whitelist(models.TriggerColumns.Status)); err != nil {
		return richerrors.Error{
			ExternalMsg: "Error deleting trigger",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}

	// Delete all related vehicle subscriptions
	if _, err := models.VehicleSubscriptions(
		models.VehicleSubscriptionWhere.TriggerID.EQ(trigger.ID),
	).DeleteAll(ctx, tx); err != nil {
		return richerrors.Error{
			ExternalMsg: "Error deleting trigger",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}

	if err := tx.Commit(); err != nil {
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
		return nil, richerrors.Error{
			ExternalMsg: "Failed to create vehicle subscription",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
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
		qm.Where(fmt.Sprintf("%s != ?", models.TriggerTableColumns.Status), StatusDeleted),
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
	deleteCount, err := models.VehicleSubscriptions(
		models.VehicleSubscriptionWhere.TriggerID.EQ(triggerID),
		models.VehicleSubscriptionWhere.VehicleTokenID.EQ(bigIntToDecimal(vehicleTokenID)),
	).DeleteAll(ctx, r.db)
	if err != nil {
		return 0, richerrors.Error{
			ExternalMsg: "Failed to delete vehicle subscription",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}
	return deleteCount, nil
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
	deleteCount, err := models.VehicleSubscriptions(
		models.VehicleSubscriptionWhere.TriggerID.EQ(triggerID),
	).DeleteAll(ctx, r.db)
	if err != nil {
		return 0, richerrors.Error{
			ExternalMsg: "Failed to delete vehicle subscriptions",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}
	return deleteCount, nil
}

// GetWebhookOwner returns the developer license address of the webhook owner
func (r *Repository) GetWebhookOwner(ctx context.Context, triggerID string) (common.Address, error) {
	trigger, err := models.Triggers(
		models.TriggerWhere.ID.EQ(triggerID),
		models.TriggerWhere.Status.NEQ(StatusDeleted),
	).One(ctx, r.db)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return common.Address{}, richerrors.Error{
				ExternalMsg: "Webhook not found",
				Err:         err,
				Code:        http.StatusNotFound,
			}
		}
		return common.Address{}, richerrors.Error{
			ExternalMsg: "Failed to get webhook owner",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}

	return common.BytesToAddress(trigger.DeveloperLicenseAddress), nil
}

// InternalGetAllVehicleSubscriptions returns all vehicle subscriptions.
// This should not be used with handler calls. Instead use GetVehicleSubscriptionsByVehicleAndDeveloperLicense.
func (r *Repository) InternalGetAllVehicleSubscriptions(ctx context.Context) ([]*models.VehicleSubscription, error) {
	subs, err := models.VehicleSubscriptions(
		qm.InnerJoin(fmt.Sprintf("%s.%s on %s = %s",
			schemaName,
			models.TableNames.Triggers,
			models.TriggerTableColumns.ID,
			models.VehicleSubscriptionTableColumns.TriggerID,
		)),
		qm.Where(fmt.Sprintf("%s != ?", models.TriggerTableColumns.Status), StatusDeleted),
	).All(ctx, r.db)
	if err != nil {
		return nil, richerrors.Error{
			ExternalMsg: "Failed to get all vehicle subscriptions",
			Err:         fmt.Errorf("failed to get all vehicle subscriptions: %w", err),
			Code:        http.StatusInternalServerError,
		}
	}
	return subs, nil
}

// InternalGetTriggerByID retrieves a specific trigger by ID.
// This should not be used with handler calls. Instead use GetTriggerByIDAndDeveloperLicense.
func (r *Repository) InternalGetTriggerByID(ctx context.Context, triggerID string) (*models.Trigger, error) {
	if triggerID == "" {
		return nil, richerrors.Error{
			ExternalMsg: "Trigger id is required",
			Err:         ValidationError,
			Code:        http.StatusBadRequest,
		}
	}

	trigger, err := models.Triggers(
		models.TriggerWhere.ID.EQ(triggerID),
		models.TriggerWhere.Status.NEQ(StatusDeleted),
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

// GetLastTriggeredAt returns the last triggered at timestamp for a trigger and vehicle token ID.
func (r *Repository) GetLastTriggeredAt(ctx context.Context, triggerID string, vehicleTokenID *big.Int) (time.Time, error) {
	logs, err := models.TriggerLogs(
		models.TriggerLogWhere.TriggerID.EQ(triggerID),
		models.TriggerLogWhere.VehicleTokenID.EQ(bigIntToDecimal(vehicleTokenID)),
		qm.OrderBy("last_triggered_at DESC"),
	).One(ctx, r.db)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, nil
		}
		return time.Time{}, richerrors.Error{
			ExternalMsg: "Failed to get last triggered at",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}
	return logs.LastTriggeredAt, nil
}

// CreateTriggerLog creates a new trigger log.
func (r *Repository) CreateTriggerLog(ctx context.Context, log *models.TriggerLog) error {
	if err := log.Insert(ctx, r.db, boil.Infer()); err != nil {
		return richerrors.Error{
			ExternalMsg: "Failed to create trigger log",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}
	return nil
}
func bigIntToDecimal(vehicleTokenID *big.Int) types.Decimal {
	dec := types.NewDecimal(new(decimal.Big))
	dec.SetBigMantScale(vehicleTokenID, 0)
	return dec
}

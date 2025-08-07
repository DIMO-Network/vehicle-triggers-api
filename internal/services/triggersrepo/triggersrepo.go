package triggersrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/ericlagergren/decimal"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/volatiletech/null/v8"
	"github.com/volatiletech/sqlboiler/v4/boil"
	"github.com/volatiletech/sqlboiler/v4/queries/qm"
	"github.com/volatiletech/sqlboiler/v4/types"
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

// CreateTrigger creates a new trigger/webhook.
func (r *Repository) CreateTrigger(ctx context.Context, req CreateTriggerRequest) (*models.Trigger, error) {
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
		return nil, err
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
		return nil, err
	}

	if triggers == nil {
		triggers = make([]*models.Trigger, 0)
	}

	return triggers, nil
}

// GetTriggerByIDAndDeveloperLicense retrieves a specific trigger by ID and developer license.
func (r *Repository) GetTriggerByIDAndDeveloperLicense(ctx context.Context, triggerID string, developerLicenseAddress common.Address) (*models.Trigger, error) {
	trigger, err := models.Triggers(
		models.TriggerWhere.ID.EQ(triggerID),
		models.TriggerWhere.DeveloperLicenseAddress.EQ(developerLicenseAddress.Bytes()),
	).One(ctx, r.db)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	return trigger, nil
}

// UpdateTrigger updates an existing trigger.
func (r *Repository) UpdateTrigger(ctx context.Context, trigger *models.Trigger) error {
	_, err := trigger.Update(ctx, r.db, boil.Infer())
	return err
}

// DeleteTrigger deletes a trigger by ID and developer license
func (r *Repository) DeleteTrigger(ctx context.Context, triggerID string, developerLicenseAddress common.Address) error {
	trigger, err := models.Triggers(
		models.TriggerWhere.ID.EQ(triggerID),
		models.TriggerWhere.DeveloperLicenseAddress.EQ(developerLicenseAddress.Bytes()),
	).One(ctx, r.db)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}

	_, err = trigger.Delete(ctx, r.db)
	return err
}

// Vehicle Subscription operations

// CreateVehicleSubscription creates a new vehicle subscription
func (r *Repository) CreateVehicleSubscription(ctx context.Context, vehicleTokenID *big.Int, triggerID string) (*models.VehicleSubscription, error) {
	subscription := &models.VehicleSubscription{
		VehicleTokenID: bigIntToDecimal(vehicleTokenID),
		TriggerID:      triggerID,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	if err := subscription.Insert(ctx, r.db, boil.Infer()); err != nil {
		return nil, err
	}

	return subscription, nil
}

// GetVehicleSubscriptionsByTriggerID retrieves all vehicle subscriptions for a trigger
func (r *Repository) GetVehicleSubscriptionsByTriggerIDs(ctx context.Context, triggerIDs ...string) ([]*models.VehicleSubscription, error) {
	subscriptions, err := models.VehicleSubscriptions(
		models.VehicleSubscriptionWhere.TriggerID.IN(triggerIDs),
	).All(ctx, r.db)

	if err != nil {
		return nil, err
	}

	return subscriptions, nil
}

// GetVehicleSubscriptionsByVehicleTokenID retrieves all subscriptions for a vehicle.
func (r *Repository) GetVehicleSubscriptionsByVehicleTokenID(ctx context.Context, vehicleTokenID *big.Int) ([]*models.VehicleSubscription, error) {
	dec := bigIntToDecimal(vehicleTokenID)
	subscriptions, err := models.VehicleSubscriptions(
		models.VehicleSubscriptionWhere.VehicleTokenID.EQ(dec),
		qm.Load(models.VehicleSubscriptionRels.Trigger),
	).All(ctx, r.db)

	if err != nil {
		return nil, err
	}

	return subscriptions, nil
}

// DeleteVehicleSubscription deletes a specific vehicle subscription.
func (r *Repository) DeleteVehicleSubscription(ctx context.Context, triggerID string, vehicleTokenID *big.Int) (int64, error) {
	return models.VehicleSubscriptions(
		models.VehicleSubscriptionWhere.TriggerID.EQ(triggerID),
		models.VehicleSubscriptionWhere.VehicleTokenID.EQ(bigIntToDecimal(vehicleTokenID)),
	).DeleteAll(ctx, r.db)
}

// DeleteAllVehicleSubscriptionsForTrigger deletes all vehicle subscriptions for a trigger.
func (r *Repository) DeleteAllVehicleSubscriptionsForTrigger(ctx context.Context, triggerID string) (int64, error) {
	return models.VehicleSubscriptions(
		models.VehicleSubscriptionWhere.TriggerID.EQ(triggerID),
	).DeleteAll(ctx, r.db)
}

// BulkCreateVehicleSubscriptions creates multiple vehicle subscriptions.
func (r *Repository) BulkCreateVehicleSubscriptions(ctx context.Context, vehicleTokenIDs []*big.Int, triggerID string) error {
	for _, vehicleTokenID := range vehicleTokenIDs {
		subscription := &models.VehicleSubscription{
			VehicleTokenID: bigIntToDecimal(vehicleTokenID),
			TriggerID:      triggerID,
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		}

		if err := subscription.Insert(ctx, r.db, boil.Infer()); err != nil {
			return err
		}
	}

	return nil
}

// BulkDeleteVehicleSubscriptions deletes multiple vehicle subscriptions.
func (r *Repository) BulkDeleteVehicleSubscriptions(ctx context.Context, vehicleTokenIDs []*big.Int, triggerID string) (int64, error) {
	var totalDeleted int64
	var errs error
	for _, vehicleTokenID := range vehicleTokenIDs {
		deleted, err := models.VehicleSubscriptions(
			models.VehicleSubscriptionWhere.TriggerID.EQ(triggerID),
			models.VehicleSubscriptionWhere.VehicleTokenID.EQ(bigIntToDecimal(vehicleTokenID)),
		).DeleteAll(ctx, r.db)

		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("error deleting vehicle subscription for vehicle token ID %s: %w", vehicleTokenID, err))
		}

		totalDeleted += deleted
	}

	return totalDeleted, nil
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

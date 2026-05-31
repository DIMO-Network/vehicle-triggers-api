package triggersrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/aarondl/null/v8"
	"github.com/aarondl/sqlboiler/v4/boil"
	"github.com/aarondl/sqlboiler/v4/queries/qm"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
)

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
	currTime := time.Now().UTC()

	// Per-trigger HMAC signing secret. 32 bytes (256 bits) is enough margin
	// for collision-resistant HMAC-SHA256. The plaintext value is returned
	// in the registration response exactly once; the column stores the
	// cipher-encrypted form so a DB compromise doesn't leak signing keys.
	plaintextSecret, err := randomHex(32)
	if err != nil {
		return nil, richerrors.Error{
			ExternalMsg: "Failed to generate signing secret",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}
	storedSecret, err := r.encryptSecret(plaintextSecret)
	if err != nil {
		return nil, richerrors.Error{
			ExternalMsg: "Failed to encrypt signing secret",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
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
		SigningSecret:           null.StringFrom(storedSecret),
		CreatedAt:               currTime,
		UpdatedAt:               currTime,
	}

	if err := trigger.Insert(ctx, r.db, boil.Infer()); err != nil {
		if isDuplicateDisplayNameError(err) {
			return nil, richerrors.Error{
				ExternalMsg: "Display name must be unique",
				Err:         err,
				Code:        http.StatusBadRequest,
			}
		}
		return nil, richerrors.Error{
			ExternalMsg: "Error during creation",
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
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, richerrors.Error{
			ExternalMsg: "Error starting transaction",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}
	defer RollbackTx(ctx, tx)
	trigger, err := r.getTriggerByIDAndDeveloperLicense(tx, ctx, triggerID, developerLicenseAddress, false)
	if err != nil {
		return nil, err
	}
	err = tx.Commit()
	if err != nil {
		return nil, richerrors.Error{
			ExternalMsg: "Error committing transaction",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}
	return trigger, nil
}

// GetTriggerByIDAndDeveloperLicenseForUpdate retrieves a specific trigger by ID and developer license in the given transaction for update.
func (r *Repository) GetTriggerByIDAndDeveloperLicenseForUpdate(ctx context.Context, triggerID string, developerLicenseAddress common.Address) (*models.Trigger, *sql.Tx, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelReadCommitted,
	})
	if err != nil {
		return nil, nil, richerrors.Error{
			ExternalMsg: "Error starting transaction",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}
	trigger, err := r.getTriggerByIDAndDeveloperLicense(tx, ctx, triggerID, developerLicenseAddress, true)
	if err != nil {
		return nil, nil, err
	}
	return trigger, tx, nil
}

func (r *Repository) getTriggerByIDAndDeveloperLicense(tx *sql.Tx, ctx context.Context, triggerID string, developerLicenseAddress common.Address, forUpdate bool) (*models.Trigger, error) {
	if triggerID == "" {
		return nil, richerrors.Error{
			ExternalMsg: "Webhook id is required",
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
	mods := []qm.QueryMod{
		models.TriggerWhere.ID.EQ(triggerID),
		models.TriggerWhere.DeveloperLicenseAddress.EQ(developerLicenseAddress.Bytes()),
		models.TriggerWhere.Status.NEQ(StatusDeleted),
	}
	if forUpdate {
		mods = append(mods, qm.For("UPDATE"))
	}
	trigger, err := models.Triggers(mods...).One(ctx, tx)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, richerrors.Error{
				ExternalMsg: "Webhook not found",
				Err:         err,
				Code:        http.StatusNotFound,
			}
		}
		return nil, richerrors.Error{
			ExternalMsg: "Error getting trigger",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}

	return trigger, nil
}

func (r *Repository) UpdateTrigger(ctx context.Context, trigger *models.Trigger) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return richerrors.Error{
			ExternalMsg: "Error starting transaction",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}
	defer RollbackTx(ctx, tx)
	err = r.updateTrigger(tx, ctx, trigger)
	if err != nil {
		return err
	}
	err = tx.Commit()
	if err != nil {
		return richerrors.Error{
			ExternalMsg: "Failed to commit Update.",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}
	return nil
}

func (r *Repository) UpdateTriggerWithTx(ctx context.Context, tx *sql.Tx, trigger *models.Trigger) error {
	return r.updateTrigger(tx, ctx, trigger)
}

func (r *Repository) updateTrigger(tx *sql.Tx, ctx context.Context, trigger *models.Trigger) error {
	trigger.UpdatedAt = time.Now().UTC()
	ret, err := trigger.Update(ctx, tx, boil.Blacklist(models.TriggerColumns.ID,
		models.TriggerColumns.ID,
		models.TriggerColumns.DeveloperLicenseAddress,
		models.TriggerColumns.Service,
		models.TriggerColumns.CreatedAt,
	))
	if err != nil {
		if isDuplicateDisplayNameError(err) {
			return richerrors.Error{
				ExternalMsg: "Display name must be unique",
				Err:         err,
				Code:        http.StatusBadRequest,
			}
		}

		return richerrors.Error{
			ExternalMsg: "Error updating trigger",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}
	if ret == 0 {
		return richerrors.Error{
			ExternalMsg: "Webhook not found",
			Err:         sql.ErrNoRows,
			Code:        http.StatusNotFound,
		}
	}
	return nil
}

// DeleteTrigger soft-deletes a trigger and removes all of its vehicle
// subscriptions in one transaction. The trigger row stays so audit / billing
// can still resolve historical fires.
func (r *Repository) DeleteTrigger(ctx context.Context, triggerID string, developerLicenseAddress common.Address) error {
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
	defer RollbackTx(ctx, tx)

	trigger.Status = StatusDeleted
	trigger.UpdatedAt = time.Now().UTC()
	if _, err := trigger.Update(ctx, tx, boil.Whitelist(models.TriggerColumns.Status, models.TriggerColumns.UpdatedAt)); err != nil {
		return richerrors.Error{
			ExternalMsg: "Error deleting trigger",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}

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

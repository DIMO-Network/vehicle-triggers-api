package triggersrepo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/migrations"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/aarondl/sqlboiler/v4/boil"
	"github.com/aarondl/sqlboiler/v4/queries/qm"
	"github.com/ethereum/go-ethereum/common"
	"github.com/lib/pq"
)

// CreateVehicleSubscription creates a new vehicle subscription.
func (r *Repository) CreateVehicleSubscription(ctx context.Context, assetDid cloudevent.ERC721DID, triggerID string) (*models.VehicleSubscription, error) {
	if assetDid == (cloudevent.ERC721DID{}) {
		return nil, richerrors.Error{
			ExternalMsg: "Asset DID is required",
			Err:         ValidationError,
			Code:        http.StatusBadRequest,
		}
	}
	if triggerID == "" {
		return nil, richerrors.Error{
			ExternalMsg: "Webhook id is required",
			Err:         ValidationError,
			Code:        http.StatusBadRequest,
		}
	}

	subscription := &models.VehicleSubscription{
		AssetDid:  assetDid.String(),
		TriggerID: triggerID,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	if err := subscription.Insert(ctx, r.db, boil.Infer()); err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) {
			if pqErr.Code == ForeignKeyViolation {
				return nil, richerrors.Error{
					ExternalMsg: "Webhook not found",
					Err:         err,
					Code:        http.StatusNotFound,
				}
			}
			if pqErr.Code == DuplicateKeyError {
				return nil, richerrors.Error{
					ExternalMsg: "Already subscribed",
					Err:         err,
					Code:        http.StatusBadRequest,
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

// GetVehicleSubscriptionsByTriggerID retrieves all vehicle subscriptions for a trigger.
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

// GetVehicleSubscriptionsByVehicleAndDeveloperLicense retrieves all
// subscriptions for trigger IDs owned by developerLicenseAddress.
func (r *Repository) GetVehicleSubscriptionsByVehicleAndDeveloperLicense(ctx context.Context, assetDid cloudevent.ERC721DID, developerLicenseAddress common.Address) ([]*models.VehicleSubscription, error) {
	if developerLicenseAddress == (common.Address{}) {
		return nil, richerrors.Error{
			ExternalMsg: "Developer license address is required",
			Err:         ValidationError,
			Code:        http.StatusBadRequest,
		}
	}
	if assetDid == (cloudevent.ERC721DID{}) {
		return nil, richerrors.Error{
			ExternalMsg: "Asset DID is required",
			Err:         ValidationError,
			Code:        http.StatusBadRequest,
		}
	}
	subscriptions, err := models.VehicleSubscriptions(
		models.VehicleSubscriptionWhere.AssetDid.EQ(assetDid.String()),
		qm.InnerJoin(fmt.Sprintf("%s.%s on %s = %s",
			migrations.SchemaName,
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
func (r *Repository) DeleteVehicleSubscription(ctx context.Context, triggerID string, assetDid cloudevent.ERC721DID) (int64, error) {
	if triggerID == "" {
		return 0, richerrors.Error{
			ExternalMsg: "Trigger id is required",
			Err:         ValidationError,
			Code:        http.StatusBadRequest,
		}
	}
	if assetDid == (cloudevent.ERC721DID{}) {
		return 0, richerrors.Error{
			ExternalMsg: "Asset DID is required",
			Err:         ValidationError,
			Code:        http.StatusBadRequest,
		}
	}
	deleteCount, err := models.VehicleSubscriptions(
		models.VehicleSubscriptionWhere.TriggerID.EQ(triggerID),
		models.VehicleSubscriptionWhere.AssetDid.EQ(assetDid.String()),
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

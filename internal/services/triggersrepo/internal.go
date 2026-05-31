package triggersrepo

import (
	"context"
	"fmt"
	"net/http"

	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/migrations"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/aarondl/sqlboiler/v4/queries/qm"
)

// InternalGetAllVehicleSubscriptions returns every active subscription joined
// against its trigger. Used by the cache rebuild; never call from a request
// handler - it has no developer-license scoping and the result set is the
// full configured fleet.
func (r *Repository) InternalGetAllVehicleSubscriptions(ctx context.Context) ([]*models.VehicleSubscription, error) {
	subs, err := models.VehicleSubscriptions(
		qm.InnerJoin(fmt.Sprintf("%s.%s on %s = %s",
			migrations.SchemaName,
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

// InternalGetTriggerByID retrieves a specific trigger by ID with no developer
// scoping. Used by the dispatcher and cache to rehydrate trigger rows after
// a CRUD-broadcast invalidation. Never call from a request handler; use
// GetTriggerByIDAndDeveloperLicense for owner-scoped reads.
func (r *Repository) InternalGetTriggerByID(ctx context.Context, triggerID string) (*models.Trigger, error) {
	if triggerID == "" {
		return nil, richerrors.Error{
			ExternalMsg: "Webhook id is required",
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

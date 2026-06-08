package triggersrepo

import (
	"context"
	"fmt"
	"net/http"

	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/ethereum/go-ethereum/common"
	"github.com/rs/zerolog"
)

// ResetTriggerFailureCount zeroes the circuit-breaker counter and re-enables
// the trigger if it was previously knocked into StatusFailed. Called by the
// dispatcher on every successful delivery; cheap when there's nothing to
// reset (single SELECT, then no UPDATE).
func (r *Repository) ResetTriggerFailureCount(ctx context.Context, trigger *models.Trigger) error {
	updatedTrigger, tx, err := r.GetTriggerByIDAndDeveloperLicenseForUpdate(ctx, trigger.ID, common.BytesToAddress(trigger.DeveloperLicenseAddress))
	if err != nil {
		return fmt.Errorf("failed to fetch trigger for success reset: %w", err)
	}
	defer RollbackTx(ctx, tx)

	if updatedTrigger.FailureCount < 1 {
		return nil
	}

	updatedTrigger.FailureCount = 0
	if updatedTrigger.Status == StatusFailed {
		updatedTrigger.Status = StatusEnabled
	}

	if err := r.UpdateTriggerWithTx(ctx, tx, updatedTrigger); err != nil {
		return fmt.Errorf("failed to reset failure count: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return richerrors.Error{
			ExternalMsg: "Failed to commit Update.",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}

	return nil
}

// IncrementTriggerFailureCount bumps the per-trigger failure counter and
// flips the trigger to StatusFailed when it hits maxFailureCount. The
// circuit-breaker check on the listener / dispatcher side reads Status +
// FailureCount; once failed, the trigger is dropped from the cache on the
// next refresh.
func (r *Repository) IncrementTriggerFailureCount(ctx context.Context, trigger *models.Trigger, failureReason error, maxFailureCount int) error {
	updatedTrigger, tx, err := r.GetTriggerByIDAndDeveloperLicenseForUpdate(ctx, trigger.ID, common.BytesToAddress(trigger.DeveloperLicenseAddress))
	if err != nil {
		return fmt.Errorf("failed to fetch trigger for failure handling: %w", err)
	}
	defer RollbackTx(ctx, tx)

	updatedTrigger.FailureCount++

	if updatedTrigger.FailureCount >= maxFailureCount {
		updatedTrigger.Status = StatusFailed
		zerolog.Ctx(ctx).Warn().
			Str("triggerId", trigger.ID).
			Int("maxFailures", maxFailureCount).
			Msg("webhook disabled due to excessive failures")
	}

	if err := r.UpdateTriggerWithTx(ctx, tx, updatedTrigger); err != nil {
		return fmt.Errorf("failed to update failure count: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return richerrors.Error{
			ExternalMsg: "Failed to commit Update.",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}

	return nil
}

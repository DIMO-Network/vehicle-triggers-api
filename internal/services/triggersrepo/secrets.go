package triggersrepo

import (
	"context"
	"net/http"

	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/aarondl/null/v8"
	"github.com/ethereum/go-ethereum/common"
)

// RotateSigningSecret generates a fresh per-trigger HMAC signing secret,
// writes it under the developer's ownership check, and returns the new
// value. Returns the secret only via the function result - it must be
// surfaced to the API caller exactly once. Subsequent reads via GetTrigger*
// expose only the stored value, which is the same secret until the next
// rotation.
func (r *Repository) RotateSigningSecret(ctx context.Context, triggerID string, devLicense common.Address) (string, error) {
	trigger, err := r.GetTriggerByIDAndDeveloperLicense(ctx, triggerID, devLicense)
	if err != nil {
		return "", err
	}
	secret, err := randomHex(32)
	if err != nil {
		return "", richerrors.Error{
			ExternalMsg: "Failed to generate signing secret",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}
	stored, err := r.encryptSecret(secret)
	if err != nil {
		return "", richerrors.Error{
			ExternalMsg: "Failed to encrypt signing secret",
			Err:         err,
			Code:        http.StatusInternalServerError,
		}
	}
	trigger.SigningSecret = null.StringFrom(stored)
	if err := r.UpdateTrigger(ctx, trigger); err != nil {
		return "", err
	}
	return secret, nil
}

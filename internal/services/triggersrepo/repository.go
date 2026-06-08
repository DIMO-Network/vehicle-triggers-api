// Package triggersrepo is the SQL persistence layer for triggers and the
// vehicle subscriptions that bind them to assets. Files are split by concern:
//
//	repository.go    - type, ctor, status/service constants, crypto helpers, tx helpers
//	triggers.go      - trigger CRUD (Create/Get/Update/Delete)
//	subscriptions.go - vehicle subscription CRUD
//	failures.go      - circuit-breaker failure-count accounting
//	secrets.go       - per-trigger HMAC signing-secret rotation
//	internal.go      - reads used by the cache rebuild and dispatcher;
//	                   no auth scoping, not for handler use.
package triggersrepo

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/secrets"
	"github.com/rs/zerolog"
)

const (
	// StatusEnabled is the status of a trigger that is enabled.
	StatusEnabled = "enabled"
	// StatusDisabled is the status of a trigger that is disabled.
	StatusDisabled = "disabled"
	// StatusFailed is the status of a trigger that has failed.
	StatusFailed = "failed"
	// StatusDeleted is the status of a trigger that has been deleted.
	StatusDeleted = "deleted"
)

const (
	// ServiceSignal is the service name for signal webhooks. MetricName includes the schema prefix (e.g. "vss.speed", "vss.currentLocationCoordinates").
	ServiceSignal = "signals"
	// ServiceEvent is the service name for event webhooks. MetricName is the full event name (e.g. "behavior.harshBraking", "security.isEngineBlocked").
	ServiceEvent = "events"
)

// IsSignalService returns true if service is a signal service.
func IsSignalService(service string) bool {
	return service == ServiceSignal
}

// IsEventService returns true if service is an event service.
func IsEventService(service string) bool {
	return service == ServiceEvent
}

// Repository is the SQL-backed trigger store. Construct with NewRepository
// and wire an at-rest cipher via SetCipher when SIGNING_SECRET_KEY_HEX is set.
type Repository struct {
	db     *sql.DB
	cipher secrets.Cipher
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db, cipher: secrets.Plaintext{}}
}

// Ping verifies the database connection is alive. Used by /health so K8s
// readiness probes don't keep a pod in service routing webhooks when the
// dispatcher's eventual repo writes are about to fail. Wraps the standard
// sql.DB.PingContext with the caller's deadline.
func (r *Repository) Ping(ctx context.Context) error {
	if r.db == nil {
		return fmt.Errorf("triggersrepo: nil db")
	}
	return r.db.PingContext(ctx)
}

// SetCipher wires the at-rest encryption cipher used for per-trigger
// signing secrets. Defaults to Plaintext (no encryption) so existing
// deployments behave unchanged until they configure SIGNING_SECRET_KEY_HEX.
func (r *Repository) SetCipher(c secrets.Cipher) {
	if c == nil {
		c = secrets.Plaintext{}
	}
	r.cipher = c
}

// encryptSecret applies the wired cipher to a freshly generated secret
// before it lands in the trigger row. Used by CreateTrigger and
// RotateSigningSecret.
func (r *Repository) encryptSecret(plaintext string) (string, error) {
	if r.cipher == nil {
		return plaintext, nil
	}
	return r.cipher.Encrypt(plaintext)
}

// DecryptSigningSecret is the reverse for read paths (audit, send). When no
// cipher is wired or the stored value looks like legacy plaintext, returns
// the input unchanged.
func (r *Repository) DecryptSigningSecret(stored string) (string, error) {
	if r.cipher == nil {
		return stored, nil
	}
	return r.cipher.Decrypt(stored)
}

// randomHex returns 2*n hex characters of cryptographic randomness, used for
// per-trigger HMAC signing secrets. Failure here is fatal for the create
// path - we never want to fall back to a weak secret.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// RollbackTx rolls back tx unless it has already been committed. Logs
// non-sql.ErrTxDone failures so a leaked tx surfaces in logs instead of
// silently piling onto the connection pool.
func RollbackTx(ctx context.Context, tx *sql.Tx) {
	if tx == nil {
		return
	}
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		zerolog.Ctx(ctx).Error().Err(err).Msg("failed to rollback transaction")
	}
}

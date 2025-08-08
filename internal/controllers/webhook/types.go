package webhook

import (
	"math/big"
	"time"
)

// RegisterWebhookRequest represents the payload to create a webhook trigger.
// It defines what to monitor, how often to notify, and where to send callbacks.
type RegisterWebhookRequest struct {
	// Service is the subsystem producing the metric (e.g. "vehicles").
	Service string `json:"service" validate:"required"`
	// MetricName is the fully qualified signal/metric to monitor.
	MetricName string `json:"metricName" validate:"required"`
	// Condition is a CEL expression evaluated against the metric to decide when to fire.
	Condition string `json:"condition" validate:"required"`
	// CoolDownPeriod is the minimum number of seconds between successive firings.
	CoolDownPeriod int `json:"coolDownPeriod" validate:"required"`
	// Description is an optional human-friendly explanation of the webhook.
	Description string `json:"description"`
	// TargetURL is the HTTPS endpoint that will receive webhook callbacks.
	TargetURL string `json:"targetURL" validate:"required"`
	// Status sets the initial state for the webhook (e.g. "Enabled" or "Disabled").
	Status string `json:"status"`
	// VerificationToken is the expected token that your endpoint must echo back during verification.
	VerificationToken string `json:"verificationToken" validate:"required"`
}

// RegisterWebhookResponse is returned after a webhook is successfully created.
type RegisterWebhookResponse struct {
	// ID is the unique identifier of the created webhook.
	ID string `json:"id"`
	// Message provides a brief status message for the operation.
	Message string `json:"message"`
}

// UpdateWebhookRequest represents the fields that can be modified on an existing webhook.
// All fields are optional; only provided fields will be updated.
type UpdateWebhookRequest struct {
	// MetricName updates the signal/event name used by the webhook.
	MetricName *string `json:"metricName"`
	// Condition updates the CEL expression used to decide when to fire.
	Condition *string `json:"condition"`
	// CoolDownPeriod updates the minimum number of seconds between firings.
	CoolDownPeriod *int `json:"coolDownPeriod"`
	// TargetURL updates the HTTPS endpoint that will receive callbacks.
	TargetURL *string `json:"targetURL"`
	// Status updates the current state of the webhook (e.g. "Enabled" or "Disabled").
	Status *string `json:"status"`
	// Description updates the optional human-friendly explanation of the webhook.
	Description *string `json:"description"`
}

// UpdateWebhookResponse is returned after a webhook is successfully updated.
type UpdateWebhookResponse struct {
	// ID is the unique identifier of the updated webhook.
	ID string `json:"id"`
	// Message provides a brief status message for the operation.
	Message string `json:"message"`
}

// DeleteWebhookResponse is returned after a webhook is deleted.
// GenericResponse is a simple standard response wrapper with a human-readable message.
type GenericResponse struct {
	// Message provides a brief status message for the operation.
	Message string `json:"message"`
}

// WebhookView represents a webhook as returned by the API.
// It excludes internal database-only fields.
type WebhookView struct {
	// ID is the unique identifier of the webhook.
	ID string `json:"id"`
	// Service is the subsystem producing the metric (e.g. "vehicles").
	Service string `json:"service"`
	// MetricName is the fully qualified signal/metric monitored by the webhook.
	MetricName string `json:"metricName"`
	// Condition is the CEL expression evaluated to decide when to fire.
	Condition string `json:"condition"`
	// TargetURI is the HTTPS endpoint that receives webhook callbacks.
	TargetURI string `json:"targetURI"`
	// CoolDownPeriod is the minimum number of seconds between successive firings.
	CoolDownPeriod int `json:"coolDownPeriod"`
	// Status is the current state of the webhook (e.g. "Enabled" or "Disabled").
	Status string `json:"status"`
	// Description is an optional human-friendly explanation of the webhook.
	Description string `json:"description"`
	// CreatedAt is when the webhook was created.
	CreatedAt time.Time `json:"createdAt"`
	// UpdatedAt is when the webhook was last modified.
	UpdatedAt time.Time `json:"updatedAt"`
	// FailureCount counts consecutive delivery failures for observability.
	FailureCount int `json:"failureCount"`
}

// SignalDefinition describes a telemetry signal available for use with webhooks.
type SignalDefinition struct {
	// Name is the JSON-safe name of the signal.
	Name string `json:"name"`
	// Description briefly explains what the signal represents.
	Description string `json:"description"`
	// Unit is the unit of measurement for the signal value (if any).
	Unit string `json:"unit"`
}

// SubscriptionView describes a vehicle's subscription to a webhook.
type SubscriptionView struct {
	// webhookID is the identifier of the webhook trigger.
	WebhookID string `json:"webhookId"`
	// VehicleTokenID is the on-chain token ID of the vehicle.
	VehicleTokenID string `json:"vehicleTokenId"`
	// CreatedAt is when the subscription was created.
	CreatedAt time.Time `json:"createdAt"`
	// Description is the optional description from the webhook trigger.
	Description string `json:"description"`
}

// FailedSubscription is a single failed subscription.
type FailedSubscription struct {
	// TokenID is the token ID of the vehicle that failed to subscribe.
	VehicleTokenID *big.Int `json:"vehicleTokenId"`
	// Message is the error message from the failed subscription.
	Message string `json:"message"`
}

// FailedSubscriptionResponse is the response to a failed subscription.
type FailedSubscriptionResponse struct {
	// FailedSubscriptions is the list of failed subscriptions.
	FailedSubscriptions []FailedSubscription `json:"failedSubscriptions"`
}

type VehicleListRequest struct {
	// VehicleTokenIDs is the list of vehicle token IDs to subscribe to the webhook.
	VehicleTokenIDs []*big.Int `json:"vehicleTokenIds"`
}

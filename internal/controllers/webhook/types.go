package webhook

import (
	"time"

	"github.com/DIMO-Network/cloudevent"
)

// RegisterWebhookRequest represents the payload to create a webhook trigger.
// It defines what to monitor, how often to notify, and where to send callbacks.
type RegisterWebhookRequest struct {
	// Service is the subsystem producing the metric (e.g. "telemetry.signals or telemetry.events").
	// This field can not be updated after the webhook is created.
	Service string `json:"service" validate:"required" example:"telemetry.signals"`
	// MetricName is the fully qualified event/signal to monitor.
	// This field can not be updated after the webhook is created.
	MetricName string `json:"metricName" validate:"required" example:"speed"`
	// Condition is a CEL expression evaluated against the metric to decide when to fire.
	Condition string `json:"condition" validate:"required" example:"valueNumber > 55"`
	// CoolDownPeriod is the minimum number of seconds between successive firings.
	CoolDownPeriod int `json:"coolDownPeriod" validate:"required" example:"30"`
	// Description is an optional human-friendly explanation of the webhook.
	Description string `json:"description" example:"This webhook is used to notify when the speed of the vehicle exceeds 55 mph."`
	// DisplayName is a user-friendly unique name per developer license.
	// if not provided, it will be set the to the Id of the webhook.
	DisplayName string `json:"displayName" example:"Speed Alert"`
	// TargetURL is the HTTPS endpoint that will receive webhook callbacks.
	TargetURL string `json:"targetURL" validate:"required" example:"https://example.com/webhook"`
	// Status sets the initial state for the webhook (e.g. "enabled" or "Disabled").
	Status string `json:"status" example:"enabled"`
	// VerificationToken is the expected token that your endpoint must echo back during verification.
	VerificationToken string `json:"verificationToken" validate:"required" example:"1234567890"`
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
	// Condition updates the CEL expression used to decide when to fire.
	Condition *string `json:"condition"`
	// CoolDownPeriod updates the minimum number of seconds between firings.
	CoolDownPeriod *int `json:"coolDownPeriod"`
	// TargetURL updates the HTTPS endpoint that will receive callbacks.
	TargetURL *string `json:"targetURL"`
	// Status updates the current state of the webhook (e.g. "enabled" or "Disabled").
	Status *string `json:"status"`
	// Description updates the optional human-friendly explanation of the webhook.
	Description *string `json:"description"`
	// DisplayName updates the user-friendly unique name per developer license.
	DisplayName *string `json:"displayName"`
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
	// TargetURL is the HTTPS endpoint that receives webhook callbacks.
	TargetURL string `json:"targetURL"`
	// CoolDownPeriod is the minimum number of seconds between successive firings.
	CoolDownPeriod int `json:"coolDownPeriod"`
	// Status is the current state of the webhook (e.g. "enabled" or "Disabled").
	Status string `json:"status"`
	// Description is an optional human-friendly explanation of the webhook.
	Description string `json:"description"`
	// CreatedAt is when the webhook was created.
	CreatedAt time.Time `json:"createdAt"`
	// UpdatedAt is when the webhook was last modified.
	UpdatedAt time.Time `json:"updatedAt"`
	// FailureCount counts consecutive delivery failures for observability.
	FailureCount int `json:"failureCount"`
	// DisplayName is the user-friendly unique name per developer license.
	DisplayName string `json:"displayName"`
}

// WebhookPayload represents the standardized payload sent to webhook endpoints.
// This structure follows industry best practices and includes only essential information
// while providing proper context and metadata for the triggered event.
type WebhookPayload struct {
	// Service identifies the subsystem that produced the signal (e.g., "telemetry.signals")
	Service string `json:"service"`

	// MetricName is the fully qualified signal/metric monitored by the webhook.
	MetricName string `json:"metricName"`

	// WebhookId is the ID of the webhook trigger that fired
	WebhookId string `json:"webhookId"`

	// WebhookName is the user-friendly display name of the trigger
	WebhookName string `json:"webhookName"`

	// Asset contains information about the asset that generated the signal
	AssetDID cloudevent.ERC721DID `json:"assetDID"`

	// Condition is the CEL expression that was evaluated to trigger this webhook
	Condition string `json:"condition"`

	// Signal contains the specific signal data that triggered the webhook
	Signal *SignalData `json:"signal,omitempty"`

	// Event contains the event data that triggered the webhook
	Event *EventData `json:"event,omitempty"`
}

// SignalData contains the signal information that triggered the webhook
type SignalData struct {
	// Name is the signal name (e.g., "speed", "engineTemperature")
	Name string `json:"name"`
	// Units is the unit of measurement for the signal value (if any).
	Units string `json:"unit,omitempty"`
	// Timestamp is when the signal was originally captured
	Timestamp time.Time `json:"timestamp"`
	// Source identifies which oracle the signal originated from.
	Source string `json:"source,omitempty"`
	// Producer is DID of device that produced the signal.
	Producer string `json:"producer,omitempty"`
	// ValueType is the data type for the value field e.g. "float64" or "string"
	ValueType string `json:"valueType"`
	// Value contains the signal value (either number or string, depending on signal type)
	Value any `json:"value"`
}

// EventData contains the event information that triggered the webhook
type EventData struct {
	// Name is the event name (e.g., "speed", "engineTemperature")
	Name string `json:"name"`
	// Timestamp is when the event was originally captured
	Timestamp time.Time `json:"timestamp"`
	// Source identifies which oracle the event originated from.
	Source string `json:"source,omitempty"`
	// Producer is DID of device that produced the event.
	Producer string `json:"producer,omitempty"`
	// DurationNanos is the duration of the event in nanoseconds
	DurationNs int64 `json:"durationNs"`
}

// SubscriptionView describes a vehicle's subscription to a webhook.
type SubscriptionView struct {
	// webhookID is the identifier of the webhook trigger.
	WebhookID string `json:"webhookId"`
	// AssetDid is the DID of the asset tied to the subscription.
	AssetDid cloudevent.ERC721DID `json:"assetDid"`
	// CreatedAt is when the subscription was created.
	CreatedAt time.Time `json:"createdAt"`
	// Description is the optional description from the webhook trigger.
	Description string `json:"description"`
}

// FailedSubscription is a single failed subscription.
type FailedSubscription struct {
	// AssetDid is the DID of the asset that failed to subscribe.
	AssetDid cloudevent.ERC721DID `json:"assetDid"`
	// Message is the error message from the failed subscription.
	Message string `json:"message"`
}

// FailedSubscriptionResponse is the response to a failed subscription.
type FailedSubscriptionResponse struct {
	// FailedSubscriptions is the list of failed subscriptions.
	FailedSubscriptions []FailedSubscription `json:"failedSubscriptions"`
}

type VehicleListRequest struct {
	// AssetDIDs is the list of asset DIDs to subscribe to the webhook.
	AssetDIDs []cloudevent.ERC721DID `json:"assetDIDs"`
}

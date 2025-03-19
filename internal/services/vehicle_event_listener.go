package services

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/teris-io/shortid"
	"github.com/volatiletech/null/v8"
	"github.com/volatiletech/sqlboiler/v4/boil"
	"github.com/volatiletech/sqlboiler/v4/queries/qm"
	"net/http"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"

	"github.com/DIMO-Network/shared/db" // your store interface
	"github.com/DIMO-Network/vehicle-events-api/internal/db/models"
)

// Signal represents the Kafka message payload from topic.device.signals.
type Signal struct {
	TokenID      uint32    `json:"tokenId"`
	Timestamp    time.Time `json:"timestamp"`
	Name         string    `json:"name"`
	ValueNumber  float64   `json:"valueNumber"`
	ValueString  string    `json:"valueString"`
	Source       string    `json:"source"`
	Producer     string    `json:"producer"`
	CloudEventID string    `json:"cloudEventId"`
}

func generateShortID(logger zerolog.Logger) string {
	id, err := shortid.Generate()
	if err != nil {
		logger.Error().Err(err).Msg("Failed to generate short ID")
		return ""
	}
	return id
}

type SignalListener struct {
	log          zerolog.Logger
	webhookCache *WebhookCache
	store        db.Store
}

func NewSignalListener(logger zerolog.Logger, wc *WebhookCache, store db.Store) *SignalListener {
	return &SignalListener{
		log:          logger,
		webhookCache: wc,
		store:        store,
	}
}

func (l *SignalListener) ProcessSignals(messages <-chan *message.Message) {
	for msg := range messages {
		if err := l.processMessage(msg); err != nil {
			l.log.Err(err).Msg("error processing signal message")
		}
		msg.Ack()
	}
}

func (l *SignalListener) processMessage(msg *message.Message) error {
	var signal Signal
	if err := json.Unmarshal(msg.Payload, &signal); err != nil {
		return errors.Wrap(err, "failed to parse vehicle signal JSON")
	}

	/*

		l.log.Debug().
			Uint32("token_id", signal.TokenID).
			Str("signal_name", signal.Name).
			Float64("value_number", signal.ValueNumber).
			Str("value_string", signal.ValueString).
			Msg("Parsed Signal")

	*/

	webhooks := l.webhookCache.GetWebhooks(signal.TokenID, signal.Name)
	if len(webhooks) == 0 {
		return nil
	}

	for _, wh := range webhooks {
		// Check if cooldown has elapsed before evaluating condition
		cooldownPassed, err := l.checkCooldown(wh)
		if err != nil {
			l.log.Error().Err(err).Msg("failed to check cooldown")
			continue
		}
		if !cooldownPassed {
			l.log.Info().Str("webhook_url", wh.URL).Msg("Cooldown period not elapsed; skipping webhook.")
			continue
		}

		shouldFire, err := l.evaluateCondition(wh.Condition, &signal)
		if err != nil {
			l.log.Error().Err(err).Msg("failed to evaluate CEL condition")
			continue
		}
		if shouldFire {
			l.log.Info().
				Str("webhook_url", wh.URL).
				Str("condition", wh.Condition).
				Msg("Webhook triggered.")
			if err := l.sendWebhookNotification(wh.URL, &signal); err != nil {
				l.log.Error().Err(err).Msg("failed to send webhook")
			} else {
				if err := l.logWebhookTrigger(wh.ID); err != nil {
					l.log.Error().Err(err).Msg("failed to log webhook trigger")
				}
			}
		} else {
			l.log.Debug().
				Str("webhook_url", wh.URL).
				Str("condition", wh.Condition).
				Msg("Condition not met; skipping webhook.")
		}
	}
	return nil
}

// evaluateCondition uses CEL to check if the condition is satisfied by the given signal
func (l *SignalListener) evaluateCondition(cond string, signal *Signal) (bool, error) {
	if cond == "" {
		return true, nil
	}

	env, err := cel.NewEnv(
		cel.Variable("valueNumber", cel.DoubleType),
		cel.Variable("valueString", cel.StringType),
		cel.Variable("tokenId", cel.IntType),
	)
	if err != nil {
		return false, err
	}

	ast, issues := env.Compile(cond)
	if issues != nil && issues.Err() != nil {
		return false, issues.Err()
	}

	prg, err := env.Program(ast)
	if err != nil {
		return false, err
	}

	vars := map[string]interface{}{
		"valueNumber": signal.ValueNumber,
		"valueString": signal.ValueString,
		"tokenId":     int64(signal.TokenID),
	}
	out, _, err := prg.Eval(vars)
	if err != nil {
		return false, err
	}

	return out == types.True, nil
}

func (l *SignalListener) sendWebhookNotification(url string, signal *Signal) error {
	body, err := json.Marshal(signal)
	if err != nil {
		return errors.Wrap(err, "failed to marshal signal for webhook")
	}
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return errors.Wrap(err, "failed to POST to webhook")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return errors.Errorf("webhook returned status code %d", resp.StatusCode)
	}
	return nil
}

func (l *SignalListener) checkCooldown(webhook Webhook) (bool, error) {
	// Query the event_logs table for the most recent trigger record for this webhook
	logs, err := models.EventLogs(
		qm.Where("event_id = ?", webhook.ID),
		qm.OrderBy("last_triggered_at DESC"),
		qm.Limit(1),
	).All(context.Background(), l.store.DBS().Reader)
	if err != nil {
		return false, err
	}
	if len(logs) == 0 {
		return true, nil
	}
	lastTriggered := logs[0].LastTriggeredAt
	if time.Since(lastTriggered) >= time.Duration(webhook.CooldownPeriod)*time.Second {
		return true, nil
	}
	return false, nil
}

// logWebhookTrigger inserts a record into the event_logs table to mark that the webhook was triggered
func (l *SignalListener) logWebhookTrigger(eventID string) error {
	eventLog := &models.EventLog{
		ID:               generateShortID(l.log),
		EventID:          eventID,
		SnapshotData:     []byte("{}"),
		HTTPResponseCode: null.IntFrom(200),
		LastTriggeredAt:  time.Now(),
		ConditionData:    null.JSONFrom([]byte("{}")),
		EventType:        "vehicle.signal",
		PermissionStatus: "Granted",
		CreatedAt:        time.Now(),
	}
	return eventLog.Insert(context.Background(), l.store.DBS().Writer, boil.Infer())
}

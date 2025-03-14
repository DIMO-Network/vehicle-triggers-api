package services

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
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

// SignalListener consumes signals from Kafka and processes them
type SignalListener struct {
	log          zerolog.Logger
	webhookCache *WebhookCache
}

// NewSignalListener initializes a listener with a logger and an in-memory cache of webhooks
func NewSignalListener(logger zerolog.Logger, wc *WebhookCache) *SignalListener {
	return &SignalListener{
		log:          logger,
		webhookCache: wc,
	}
}

// ProcessSignals is the entry point for Watermill. It loops over incoming messages
func (l *SignalListener) ProcessSignals(messages <-chan *message.Message) {
	for msg := range messages {
		if err := l.processMessage(msg); err != nil {
			l.log.Err(err).Msg("error processing signal message")
		}
		msg.Ack()
	}
}

// processMessage parses the Signal, looks up matching webhooks, and evaluates conditions
func (l *SignalListener) processMessage(msg *message.Message) error {

	var signal Signal
	if err := json.Unmarshal(msg.Payload, &signal); err != nil {
		return errors.Wrap(err, "failed to parse vehicle signal JSON")
	}

	l.log.Debug().
		Uint32("token_id", signal.TokenID).
		Str("signal_name", signal.Name).
		Float64("value_number", signal.ValueNumber).
		Str("value_string", signal.ValueString).
		Msg("Parsed Signal")

	webhooks := l.webhookCache.GetWebhooks(signal.TokenID, signal.Name)
	if len(webhooks) == 0 {
		return nil
	}

	for _, wh := range webhooks {
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
		return true, nil // always fire
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

// sendWebhookNotification posts the signal to the given URL
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

package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/teris-io/shortid"
	"github.com/volatiletech/null/v8"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"

	"github.com/DIMO-Network/shared/db"
	"github.com/DIMO-Network/vehicle-events-api/internal/db/models"
	"github.com/volatiletech/sqlboiler/v4/boil"
	"github.com/volatiletech/sqlboiler/v4/queries/qm"
)

func generateShortID(logger zerolog.Logger) string {
	id, err := shortid.Generate()
	if err != nil {
		logger.Error().Err(err).Msg("Failed to generate short ID")
		return ""
	}
	return strings.TrimSpace(id)
}

type Signal struct {
	TokenID      uint32    `json:"token_id"`
	Timestamp    time.Time `json:"time"`
	Name         string    `json:"signal_name"`
	ValueNumber  float64   `json:"value_number"`
	ValueString  string    `json:"value_string"`
	Source       string    `json:"source"`
	Producer     string    `json:"producer"`
	CloudEventID string    `json:"cloud_event_id"`
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
		cooldownPassed, err := l.checkCooldown(wh)
		if err != nil {
			l.log.Error().Err(err).Msg("failed to check cooldown")
			continue
		}
		if !cooldownPassed {
			l.log.Info().Str("webhook_url", wh.URL).Msg("Cooldown period not elapsed; skipping webhook.")
			continue
		}

		shouldFire, err := l.evaluateCondition(wh.Trigger, &signal, wh.Data)
		if err != nil {
			l.log.Error().Err(err).Msg("failed to evaluate CEL condition")
			continue
		}
		if shouldFire {
			l.log.Info().
				Str("webhook_url", wh.URL).
				Str("trigger", wh.Trigger).
				Msg("Webhook triggered.")
			if err := l.sendWebhookNotification(wh, &signal); err != nil {
				l.log.Error().Err(err).Msg("failed to send webhook")
			} else {
				if err := l.logWebhookTrigger(wh.ID); err != nil {
					l.log.Error().Err(err).Msg("failed to log webhook trigger")
				}
			}
		} else {
			l.log.Debug().
				Str("webhook_url", wh.URL).
				Str("trigger", wh.Trigger).
				Msg("Condition not met; skipping webhook.")
		}
	}
	return nil
}

func (l *SignalListener) evaluateCondition(trigger string, signal *Signal, telemetry string) (bool, error) {
	if trigger == "" {
		return true, nil
	}
	var varType *cel.Type
	var varValue interface{}
	if strings.ToLower(telemetry) == "value_string" {
		varType = cel.StringType
		varValue = signal.ValueString
	} else {
		varType = cel.DoubleType
		varValue = signal.ValueNumber
	}

	env, err := cel.NewEnv(
		cel.Variable(telemetry, varType),
		cel.Variable("tokenId", cel.IntType),
	)
	if err != nil {
		return false, err
	}

	ast, issues := env.Compile(trigger)
	if issues != nil && issues.Err() != nil {
		return false, issues.Err()
	}

	prg, err := env.Program(ast)
	if err != nil {
		return false, err
	}

	vars := map[string]interface{}{
		telemetry: varValue,
		"tokenId": int64(signal.TokenID),
	}
	out, _, err := prg.Eval(vars)
	if err != nil {
		return false, err
	}
	return out == types.True, nil
}

func (l *SignalListener) sendWebhookNotification(wh Webhook, signal *Signal) error {
	body, err := json.Marshal(signal)
	if err != nil {
		return errors.Wrap(err, "failed to marshal signal for webhook")
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	resp, err := client.Post(wh.URL, "application/json", bytes.NewBuffer(body))
	if err != nil {
		l.log.Error().Msgf("HTTP POST error for URL %s: %v", wh.URL, err)
		l.handleWebhookFailure(wh.ID)
		return errors.Wrap(err, "failed to POST to webhook")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		l.log.Error().Msgf("Received non-200 response from %s: status %d, body: %s", wh.URL, resp.StatusCode, string(respBody))
		l.handleWebhookFailure(wh.ID)
		return fmt.Errorf("webhook returned status code %d", resp.StatusCode)
	}
	l.log.Debug().Msgf("Webhook notification sent successfully to %s", wh.URL)
	l.resetWebhookFailure(wh.ID)
	return nil
}

func (l *SignalListener) checkCooldown(webhook Webhook) (bool, error) {
	logs, err := models.EventLogs(
		qm.Where("event_id = ?", webhook.ID),
		qm.OrderBy("last_triggered_at DESC"),
		qm.Limit(1),
	).All(context.Background(), l.store.DBS().Reader)
	if err != nil {
		l.log.Error().Err(err).Msg("Error retrieving EventLogs")
		return false, err
	}
	if len(logs) == 0 {
		l.log.Debug().Msg("No previous EventLog found; cooldown passed")
		return true, nil
	}
	lastTriggered := logs[0].LastTriggeredAt
	diff := time.Since(lastTriggered)
	l.log.Debug().Msgf("Last triggered %v ago, required cooldown: %vs", diff.Seconds(), webhook.CooldownPeriod)
	if diff >= time.Duration(webhook.CooldownPeriod)*time.Second {
		return true, nil
	}
	return false, nil
}

func (l *SignalListener) logWebhookTrigger(eventID string) error {
	eventLog := &models.EventLog{
		ID:               generateShortID(l.log),
		EventID:          eventID,
		SnapshotData:     []byte("{}"),
		HTTPResponseCode: null.IntFrom(0),
		LastTriggeredAt:  time.Now(),
		EventType:        "vehicle.signal",
		PermissionStatus: "Granted",
		CreatedAt:        time.Now(),
	}
	if err := eventLog.Insert(context.Background(), l.store.DBS().Writer, boil.Infer()); err != nil {
		l.log.Error().Err(err).Msg("Error inserting EventLog")
		return err
	}
	l.log.Debug().Msgf("Logged webhook trigger for event %s", eventID)
	return nil
}

func (l *SignalListener) handleWebhookFailure(webhookID string) {
	ctx := context.Background()
	event, err := models.FindEvent(ctx, l.store.DBS().Reader, webhookID)
	if err != nil {
		l.log.Error().Err(err).Msg("handleWebhookFailure: could not fetch event")
		return
	}
	event.FailureCount += 1
	l.log.Debug().Msgf("Incremented FailureCount for webhook %s to %d", webhookID, event.FailureCount)

	if event.FailureCount >= 5 {
		event.Status = "Disabled"
		l.log.Info().Msgf("Webhook %s disabled due to excessive failures", webhookID)
	}
	if _, err := event.Update(ctx, l.store.DBS().Writer, boil.Infer()); err != nil {
		l.log.Error().Err(err).Msg("handleWebhookFailure: failed to update event failure count")
	}
}

func (l *SignalListener) resetWebhookFailure(webhookID string) {
	ctx := context.Background()
	event, err := models.FindEvent(ctx, l.store.DBS().Reader, webhookID)
	if err != nil {
		l.log.Error().Err(err).Msg("resetWebhookFailure: could not fetch event")
		return
	}
	event.FailureCount = 0
	l.log.Debug().Msgf("Reset FailureCount for webhook %s", webhookID)
	if _, err := event.Update(ctx, l.store.DBS().Writer, boil.Infer()); err != nil {
		l.log.Error().Err(err).Msg("resetWebhookFailure: failed to reset event failure count")
	}
}

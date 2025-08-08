package vehiclelistener

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"time"

	"github.com/DIMO-Network/model-garage/pkg/vss"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/clients/tokenexchange"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhookcache"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/google/cel-go/cel"
	celtypes "github.com/google/cel-go/common/types"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/volatiletech/sqlboiler/v4/types"
)

var reIntLit = regexp.MustCompile(`\b\d+\b`)

func convertIntLits(expr string) string {
	return reIntLit.ReplaceAllStringFunc(expr, func(s string) string { return s + ".0" })
}

type SignalListener struct {
	log                 zerolog.Logger
	webhookCache        *webhookcache.WebhookCache
	repo                *triggersrepo.Repository
	tokenExchangeClient *tokenexchange.Client
}

func NewSignalListener(logger zerolog.Logger, wc *webhookcache.WebhookCache, repo *triggersrepo.Repository, tokenExchangeAPI *tokenexchange.Client) *SignalListener {
	return &SignalListener{
		log:                 logger,
		webhookCache:        wc,
		repo:                repo,
		tokenExchangeClient: tokenExchangeAPI,
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
	l.log.Debug().
		RawJSON("raw_payload", msg.Payload).
		Msg("Received raw Kafka payload")
	var signal vss.Signal
	if err := json.Unmarshal(msg.Payload, &signal); err != nil {
		return fmt.Errorf("failed to parse vehicle signal JSON: %w", err)
	}

	webhooks := l.webhookCache.GetWebhooks(signal.TokenID, signal.Name)

	if len(webhooks) == 0 {
		return nil
	}

	for _, wh := range webhooks {
		bigTokenID := big.NewInt(int64(signal.TokenID))
		hasPerm, err := l.tokenExchangeClient.HasVehiclePermissions(context.Background(), bigTokenID, wh.DeveloperLicenseAddress, []string{
			"privilege:GetNonLocationHistory",
			"privilege:GetLocationHistory",
		})
		if err != nil {
			l.log.Error().Err(err).Msg("permission check failed")
			continue
		}
		if !hasPerm {
			l.log.Info().Msgf("permissions revoked for license %x on vehicle %d", wh.DeveloperLicenseAddress, signal.TokenID)
			tokenId := new(big.Int).SetUint64(uint64(signal.TokenID))
			// 1) Delete the TriggerSubscription row and check its error
			if delCount, err := l.repo.DeleteVehicleSubscription(context.Background(), wh.ID, tokenId); err != nil {
				l.log.Error().
					Err(err).
					Str("trigger_id", wh.ID).
					Uint32("vehicle_token", signal.TokenID).
					Msg("Failed to delete vehicle subscription")
			} else {
				l.log.Debug().
					Str("event_id", wh.ID).
					Uint32("vehicle_token", signal.TokenID).
					Int("rows_deleted", int(delCount)).
					Msg("Successfully removed vehicle subscription due to permission revocation")
			}

			// 2) Refresh the cache and check its error
			if err := l.webhookCache.PopulateCache(context.Background()); err != nil {
				l.log.Error().
					Err(err).
					Msg("Failed to refresh webhook cache after permission revocation")
			}

			continue
		}
		cooldownPassed, err := l.checkCooldown(wh, signal.TokenID)
		if err != nil {
			l.log.Error().Err(err).Msg("failed to check cooldown")
			continue
		}
		if !cooldownPassed {
			l.log.Info().Str("webhook_url", wh.URL).Msg("Cooldown period not elapsed; skipping webhook.")
			continue
		}

		shouldFire, err := l.evaluateCondition(wh.Condition, &signal, wh.MetricName)
		if err != nil {
			l.log.Error().Err(err).Msg("failed to evaluate CEL condition")
			continue
		}
		if shouldFire {
			l.log.Info().
				Str("webhook_url", wh.URL).
				Str("trigger", wh.Condition).
				Msg("Webhook triggered.")
			if err := l.sendWebhookNotification(wh, &signal); err != nil {
				l.log.Error().Err(err).Msg("failed to send webhook")
			} else {
				if err := l.logWebhookTrigger(wh.ID, signal.TokenID); err != nil {
					l.log.Error().Err(err).Msg("failed to log webhook trigger")
				}
			}
		} else {
			l.log.Debug().
				Str("webhook_url", wh.URL).
				Str("trigger", wh.Condition).
				Msg("Condition not met; skipping webhook.")
		}
	}
	return nil
}

func (l *SignalListener) evaluateCondition(trigger string, signal *vss.Signal, telemetry string) (bool, error) {
	if trigger == "" {
		return true, nil
	}

	trigger = convertIntLits(trigger)

	opts := []cel.EnvOption{
		cel.Variable("valueNumber", cel.DoubleType),
		cel.Variable("valueString", cel.StringType),
		cel.Variable("tokenId", cel.IntType),
	}
	if telemetry != "valueNumber" && telemetry != "valueString" {
		opts = append(opts, cel.Variable(telemetry, cel.DoubleType))
	}

	env, err := cel.NewEnv(opts...)

	if err != nil {
		return false, fmt.Errorf("failed to compile CEL expression: %w", err)
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
		"valueNumber": signal.ValueNumber,
		"valueString": signal.ValueString,
		"tokenId":     int64(signal.TokenID),
	}
	if telemetry != "valueNumber" && telemetry != "valueString" {
		vars[telemetry] = signal.ValueNumber
	}

	out, _, err := prg.Eval(vars)
	if err != nil {
		l.log.Error().Err(err).Msg("Error during CEL evaluation")
		return false, err
	}
	return out == celtypes.True, nil
}

func (l *SignalListener) checkCooldown(webhook webhookcache.Webhook, tokenID uint32) (bool, error) {
	cooldown := webhook.CooldownPeriod
	tokenIDBigInt := new(big.Int).SetUint64(uint64(tokenID))
	lastTriggered, err := l.repo.GetLastTriggeredAt(context.Background(), webhook.ID, tokenIDBigInt)
	if err != nil {
		return false, fmt.Errorf("failed to retrieve event logs: %w", err)
	}
	if time.Since(lastTriggered) >= time.Duration(cooldown)*time.Second {
		return true, nil
	}
	return false, nil
}

func (l *SignalListener) logWebhookTrigger(eventID string, tokenID uint32) error {
	var dec types.Decimal
	if err := dec.Scan(fmt.Sprint(tokenID)); err != nil {
		l.log.Error().Err(err).Msg("failed to convert tokenID")
		return err
	}
	now := time.Now()
	eventLog := &models.TriggerLog{
		ID:              uuid.New().String(),
		TriggerID:       eventID,
		VehicleTokenID:  dec,
		SnapshotData:    []byte("{}"),
		LastTriggeredAt: now,
		CreatedAt:       now,
	}
	if err := l.repo.CreateTriggerLog(context.Background(), eventLog); err != nil {
		l.log.Error().Err(err).Msg("Error inserting EventLog")
		return err
	}
	l.log.Debug().Msgf("Logged webhook trigger for event %s, vehicle %d", eventID, tokenID)
	return nil
}

func (l *SignalListener) sendWebhookNotification(wh webhookcache.Webhook, signal *vss.Signal) error {
	body, err := json.Marshal(signal)
	if err != nil {
		return fmt.Errorf("failed to marshal signal for webhook: %w", err)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(wh.URL, "application/json", bytes.NewBuffer(body))
	if err != nil {
		l.log.Error().Msgf("HTTP POST error for URL %s: %v", wh.URL, err)
		if wh.ID != "" {
			l.handleWebhookFailure(wh)
		}
		return fmt.Errorf("failed to POST to webhook: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		l.log.Error().Msgf("Received non‑200 response from %s: status %d, body: %s",
			wh.URL, resp.StatusCode, string(respBody))
		if wh.ID != "" {
			l.handleWebhookFailure(wh)
		}
		return fmt.Errorf("webhook returned status code %d", resp.StatusCode)
	}

	l.log.Debug().Msgf("Webhook notification sent successfully to %s", wh.URL)
	if wh.ID != "" {
		l.resetWebhookFailure(wh)
	}
	return nil
}

func (l *SignalListener) handleWebhookFailure(webhook webhookcache.Webhook) {
	ctx := context.Background()
	event, err := l.repo.GetTriggerByIDAndDeveloperLicense(ctx, webhook.ID, webhook.DeveloperLicenseAddress)
	if err != nil {
		l.log.Error().Err(err).Msg("handleWebhookFailure: could not fetch event")
		return
	}
	event.FailureCount += 1
	l.log.Debug().Msgf("Incremented FailureCount for webhook %s to %d", webhook.ID, event.FailureCount)

	if event.FailureCount >= 5 {
		event.Status = "Disabled"
		l.log.Info().Msgf("Webhook %s disabled due to excessive failures", webhook.ID)
	}
	if err := l.repo.UpdateTrigger(ctx, event); err != nil {
		l.log.Error().Err(err).Msg("handleWebhookFailure: failed to update event failure count")
	}
}

func (l *SignalListener) resetWebhookFailure(webhook webhookcache.Webhook) {
	ctx := context.Background()
	event, err := l.repo.GetTriggerByIDAndDeveloperLicense(ctx, webhook.ID, webhook.DeveloperLicenseAddress)
	if err != nil {
		l.log.Error().Err(err).Msg("resetWebhookFailure: could not fetch event")
		return
	}
	event.FailureCount = 0
	l.log.Debug().Msgf("Reset FailureCount for webhook %s", webhook.ID)
	if err := l.repo.UpdateTrigger(ctx, event); err != nil {
		l.log.Error().Err(err).Msg("resetWebhookFailure: failed to reset event failure count")
	}
}

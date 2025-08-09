package vehiclelistener

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"time"

	"github.com/DIMO-Network/model-garage/pkg/vss"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/celcondition"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/clients/tokenexchange"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhookcache"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/volatiletech/sqlboiler/v4/types"
	"golang.org/x/sync/errgroup"
)

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

func (l *SignalListener) ProcessSignals(ctx context.Context, messages <-chan *message.Message) {
	for msg := range messages {
		if err := l.processMessage(ctx, msg); err != nil {
			l.log.Err(err).Msg("error processing signal message")
		}
		msg.Ack()
	}
}

func (l *SignalListener) processMessage(ctx context.Context, msg *message.Message) error {
	var signal vss.Signal
	if err := json.Unmarshal(msg.Payload, &signal); err != nil {
		return fmt.Errorf("failed to parse vehicle signal JSON: %w", err)
	}

	webhooks := l.webhookCache.GetWebhooks(signal.TokenID, signal.Name)

	if len(webhooks) == 0 {
		return nil
	}

	group, _ := errgroup.WithContext(ctx)
	group.SetLimit(100)
	for _, wh := range webhooks {
		group.Go(func() error {
			if err := l.processWebhook(ctx, wh, &signal); err != nil {
				zerolog.Ctx(ctx).Error().Err(err).Msg("failed to process webhook")
			}
			return nil
		})
	}
	return group.Wait()
}

func (l *SignalListener) processWebhook(ctx context.Context, wh *webhookcache.Webhook, signal *vss.Signal) error {
	bigTokenID := big.NewInt(int64(signal.TokenID))
	hasPerm, err := l.tokenExchangeClient.HasVehiclePermissions(ctx, bigTokenID, common.BytesToAddress(wh.Trigger.DeveloperLicenseAddress), []string{
		"privilege:GetNonLocationHistory",
		"privilege:GetLocationHistory",
	})
	if err != nil {
		return fmt.Errorf("permission check failed: %w", err)
	}
	if !hasPerm {
		// If we don't have permission, unsubscribe from the trigger and refresh the cache
		zerolog.Ctx(ctx).Info().Msgf("permissions revoked for license %x on vehicle %d", wh.Trigger.DeveloperLicenseAddress, signal.TokenID)
		tokenId := new(big.Int).SetUint64(uint64(signal.TokenID))
		_, err := l.repo.DeleteVehicleSubscription(context.Background(), wh.Trigger.ID, tokenId)
		if err != nil {
			return fmt.Errorf("failed to delete vehicle subscription: %w", err)
		}
		l.webhookCache.ScheduleRefresh(ctx)
		return nil
	}
	cooldownPassed, err := l.checkCooldown(wh.Trigger, signal.TokenID)
	if err != nil {
		return fmt.Errorf("failed to check cooldown: %w", err)
	}
	if !cooldownPassed {
		// If the cooldown period hasn't passed, skip the webhook
		return nil
	}

	shouldFire, err := celcondition.EvaluateCondition(wh.Program, signal)
	if err != nil {
		return fmt.Errorf("failed to evaluate CEL condition: %w", err)
	}
	if !shouldFire {
		return nil
	}

	if err := l.sendWebhookNotification(wh.Trigger, signal); err != nil {
		return fmt.Errorf("failed to send webhook: %w", err)
	}
	if err := l.logWebhookTrigger(wh.Trigger.ID, signal.TokenID); err != nil {
		return fmt.Errorf("failed to log webhook trigger: %w", err)
	}

	return nil
}

func (l *SignalListener) checkCooldown(webhook *models.Trigger, tokenID uint32) (bool, error) {
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

func (l *SignalListener) sendWebhookNotification(wh *models.Trigger, signal *vss.Signal) error {
	body, err := json.Marshal(signal)
	if err != nil {
		return fmt.Errorf("failed to marshal signal for webhook: %w", err)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(wh.TargetURI, "application/json", bytes.NewBuffer(body))
	if err != nil {
		l.log.Error().Msgf("HTTP POST error for URL %s: %v", wh.TargetURI, err)
		if wh.ID != "" {
			l.handleWebhookFailure(wh)
		}
		return fmt.Errorf("failed to POST to webhook: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		l.log.Error().Msgf("Received non‑200 response from %s: status %d, body: %s",
			wh.TargetURI, resp.StatusCode, string(respBody))
		if wh.ID != "" {
			l.handleWebhookFailure(wh)
		}
		return fmt.Errorf("webhook returned status code %d", resp.StatusCode)
	}

	l.log.Debug().Msgf("Webhook notification sent successfully to %s", wh.TargetURI)
	if wh.ID != "" {
		l.resetWebhookFailure(wh)
	}
	return nil
}

func (l *SignalListener) handleWebhookFailure(webhook *models.Trigger) {
	ctx := context.Background()
	event, err := l.repo.GetTriggerByIDAndDeveloperLicense(ctx, webhook.ID, common.BytesToAddress(webhook.DeveloperLicenseAddress))
	if err != nil {
		l.log.Error().Err(err).Msg("handleWebhookFailure: could not fetch event")
		return
	}
	event.FailureCount += 1
	l.log.Debug().Msgf("Incremented FailureCount for webhook %s to %d", webhook.ID, event.FailureCount)

	if event.FailureCount >= 5 {
		event.Status = triggersrepo.StatusFailed
		l.log.Info().Msgf("Webhook %s disabled due to excessive failures", webhook.ID)
	}
	if err := l.repo.UpdateTrigger(ctx, event); err != nil {
		l.log.Error().Err(err).Msg("handleWebhookFailure: failed to update event failure count")
	}
}

func (l *SignalListener) resetWebhookFailure(webhook *models.Trigger) {
	ctx := context.Background()
	event, err := l.repo.GetTriggerByIDAndDeveloperLicense(ctx, webhook.ID, common.BytesToAddress(webhook.DeveloperLicenseAddress))
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

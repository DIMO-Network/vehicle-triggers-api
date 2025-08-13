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

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/model-garage/pkg/vss"
	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/celcondition"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/clients/tokenexchange"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/config"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/webhook"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhookcache"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/signals"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ericlagergren/decimal"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/volatiletech/sqlboiler/v4/types"
	"golang.org/x/sync/errgroup"
)

const (
	webhookFailureCode = -1
)

type SignalListener struct {
	webhookCache        *webhookcache.WebhookCache
	repo                *triggersrepo.Repository
	tokenExchangeClient *tokenexchange.Client
	vehicleNFTAddress   common.Address
	dimoRegistryChainID uint64
}

func NewSignalListener(wc *webhookcache.WebhookCache, repo *triggersrepo.Repository, tokenExchangeAPI *tokenexchange.Client, settings *config.Settings) *SignalListener {
	return &SignalListener{
		webhookCache:        wc,
		repo:                repo,
		tokenExchangeClient: tokenExchangeAPI,
		vehicleNFTAddress:   settings.VehicleNFTAddress,
		dimoRegistryChainID: settings.DIMORegistryChainID,
	}
}

func (l *SignalListener) ProcessSignals(ctx context.Context, messages <-chan *message.Message) {
	logger := zerolog.Ctx(ctx)
	for msg := range messages {
		msg.SetContext(ctx)
		if err := l.processMessage(msg); err != nil {
			logger.Error().Err(err).Msg("error processing signal message")
		}
		msg.Ack()
	}
}

func (l *SignalListener) processMessage(msg *message.Message) error {
	var signal vss.Signal
	if err := json.Unmarshal(msg.Payload, &signal); err != nil {
		return fmt.Errorf("failed to parse vehicle signal JSON: %w", err)
	}

	webhooks := l.webhookCache.GetWebhooks(signal.TokenID, signal.Name)

	if len(webhooks) == 0 {
		// no webhooks found for this signal, skip
		return nil
	}

	group, groupCtx := errgroup.WithContext(msg.Context())
	group.SetLimit(100)
	for _, wh := range webhooks {
		group.Go(func() error {
			if err := l.processWebhook(groupCtx, wh, &signal); err != nil {
				zerolog.Ctx(groupCtx).Error().Str("trigger_id", wh.Trigger.ID).Err(err).Msg("failed to process webhook")
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

	payload, err := l.sendWebhookNotification(ctx, wh.Trigger, signal)
	if err != nil {
		if richError, ok := richerrors.AsRichError(err); ok && richError.Code == webhookFailureCode {
			if err := l.handleWebhookFailure(ctx, wh.Trigger); err != nil {
				zerolog.Ctx(ctx).Error().Err(err).Msg("failed to handle webhook failure")
			}
		}
		return fmt.Errorf("failed to send webhook: %w", err)
	}
	if err := l.logWebhookTrigger(ctx, payload); err != nil {
		return fmt.Errorf("failed to log webhook trigger: %w", err)
	}

	if err := l.resetWebhookFailure(ctx, wh.Trigger); err != nil {
		return fmt.Errorf("failed to reset webhook failure: %w", err)
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

func (l *SignalListener) logWebhookTrigger(ctx context.Context, cloudEvent *cloudevent.CloudEvent[webhook.WebhookPayload]) error {
	dec := types.NewDecimal(new(decimal.Big))
	dec.SetBigMantScale(cloudEvent.Data.AssetDID.TokenID, 0)
	now := time.Now()
	eventLog := &models.TriggerLog{
		ID:              cloudEvent.ID,
		TriggerID:       cloudEvent.Data.WebhookId,
		VehicleTokenID:  dec,
		SnapshotData:    []byte("{}"),
		LastTriggeredAt: now,
		CreatedAt:       now,
	}
	if err := l.repo.CreateTriggerLog(ctx, eventLog); err != nil {
		return fmt.Errorf("failed to create trigger log: %w", err)
	}
	return nil
}

func (l *SignalListener) sendWebhookNotification(ctx context.Context, wh *models.Trigger, signal *vss.Signal) (*cloudevent.CloudEvent[webhook.WebhookPayload], error) {
	// Create the standardized webhook payload
	payload, err := l.createWebhookPayload(wh, signal)
	if err != nil {
		return nil, fmt.Errorf("failed to create webhook payload: %w", err)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal webhook payload: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(wh.TargetURI, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return nil, richerrors.Error{
			Code: webhookFailureCode,
			Err:  fmt.Errorf("failed to POST to webhook: %w", err),
		}
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, richerrors.Error{
			Code: webhookFailureCode,
			Err:  fmt.Errorf("webhook returned status code %d: %s", resp.StatusCode, string(respBody)),
		}
	}
	return payload, nil
}

// createWebhookPayload creates a standardized webhook payload following industry best practices
func (l *SignalListener) createWebhookPayload(trigger *models.Trigger, signal *vss.Signal) (*cloudevent.CloudEvent[webhook.WebhookPayload], error) {
	// Determine the signal value based on type
	var signalValue any
	signalDef, err := signals.GetSignalDefinition(signal.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to get signal definition: %w", err)
	}
	switch signalDef.Type {
	case signals.NumberType:
		signalValue = signal.ValueNumber
	case signals.StringType:
		signalValue = signal.ValueString
	default:
		return nil, fmt.Errorf("unsupported signal type: %s", signalDef.Type)
	}

	vehicleDID := cloudevent.ERC721DID{
		TokenID:         big.NewInt(int64(signal.TokenID)),
		ContractAddress: l.vehicleNFTAddress,
		ChainID:         l.dimoRegistryChainID,
	}

	return &cloudevent.CloudEvent[webhook.WebhookPayload]{
		CloudEventHeader: cloudevent.CloudEventHeader{
			ID:              uuid.New().String(),
			Source:          "vehicle-triggers-api", //TODO(kevin): Should be 0x of the storageNode
			Subject:         vehicleDID.String(),
			Time:            time.Now(),
			DataContentType: "application/json",
			DataVersion:     "telemetry.signals/v1.0",
			Type:            "dimo.trigger",
			SpecVersion:     "1.0",
			Producer:        trigger.ID,
		},
		Data: webhook.WebhookPayload{
			Service:     trigger.Service,
			MetricName:  trigger.MetricName,
			WebhookId:   trigger.ID,
			WebhookName: trigger.DisplayName,
			AssetDID:    vehicleDID,
			Condition:   trigger.Condition,
			Signal: &webhook.SignalData{
				Name:      signal.Name,
				Units:     signalDef.Unit,
				ValueType: signalDef.Type,
				Value:     signalValue,
				Timestamp: signal.Timestamp,
				Source:    signal.Source,
				Producer:  signal.Producer,
			},
		},
	}, nil
}

func (l *SignalListener) handleWebhookFailure(ctx context.Context, webhook *models.Trigger) error {
	event, err := l.repo.GetTriggerByIDAndDeveloperLicense(ctx, webhook.ID, common.BytesToAddress(webhook.DeveloperLicenseAddress))
	if err != nil {
		return fmt.Errorf("could not fetch event: %w", err)
	}
	event.FailureCount += 1

	if event.FailureCount >= 5 {
		event.Status = triggersrepo.StatusFailed
		zerolog.Ctx(ctx).Info().Msgf("Webhook %s disabled due to excessive failures", webhook.ID)
	}
	if err := l.repo.UpdateTrigger(ctx, event); err != nil {
		return fmt.Errorf("failed to update event failure count: %w", err)
	}
	return nil
}

func (l *SignalListener) resetWebhookFailure(ctx context.Context, webhook *models.Trigger) error {
	event, err := l.repo.GetTriggerByIDAndDeveloperLicense(ctx, webhook.ID, common.BytesToAddress(webhook.DeveloperLicenseAddress))
	if err != nil {
		return fmt.Errorf("could not fetch event: %w", err)
	}
	if event.FailureCount == 0 {
		// if the failure count is 0, we don't need to reset it
		return nil
	}
	event.FailureCount = 0
	zerolog.Ctx(ctx).Debug().Msgf("Reset FailureCount for webhook %s", webhook.ID)
	if err := l.repo.UpdateTrigger(ctx, event); err != nil {
		return fmt.Errorf("failed to reset event failure count: %w", err)
	}
	return nil
}

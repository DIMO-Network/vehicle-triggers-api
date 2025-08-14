package metriclistener

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/model-garage/pkg/vss"
	"github.com/DIMO-Network/server-garage/pkg/richerrors"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/clients/tokenexchange"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/config"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/webhook"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/models"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhookcache"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/volatiletech/sqlboiler/v4/types"
)

const (
	webhookFailureCode = -1
)

// EventWithRawData is a struct that contains an event and the raw data.
type EventWithRawData struct {
	Event      vss.Event
	VehicleDID cloudevent.ERC721DID
	RawData    json.RawMessage
}

type MetricListener struct {
	webhookCache        *webhookcache.WebhookCache
	repo                *triggersrepo.Repository
	tokenExchangeClient *tokenexchange.Client
	vehicleNFTAddress   common.Address
	dimoRegistryChainID uint64
}

// NewMetricsListener creates a new MetrticListener.
func NewMetricsListener(wc *webhookcache.WebhookCache, repo *triggersrepo.Repository, tokenExchangeAPI *tokenexchange.Client, settings *config.Settings) *MetricListener {
	return &MetricListener{
		webhookCache:        wc,
		repo:                repo,
		tokenExchangeClient: tokenExchangeAPI,
		vehicleNFTAddress:   settings.VehicleNFTAddress,
		dimoRegistryChainID: settings.DIMORegistryChainID,
	}
}

func (m *MetricListener) ProcessSignalMessages(ctx context.Context, messages <-chan *message.Message) error {
	return processMessage(ctx, messages, m.processSignalMessage)
}

func (m *MetricListener) ProcessEventMessages(ctx context.Context, messages <-chan *message.Message) error {
	return processMessage(ctx, messages, m.processEventMessage)
}

func processMessage(ctx context.Context, messages <-chan *message.Message, processor func(msg *message.Message) error) error {
	logger := zerolog.Ctx(ctx)
	for msg := range messages {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		msg.SetContext(ctx)
		if err := processor(msg); err != nil {
			logger.Error().Err(err).Msg("error processing signal message")
		}
		msg.Ack()
	}
	return nil
}

func (m *MetricListener) handleTriggeredWebhook(ctx context.Context, trigger *models.Trigger, lastTrigger *models.TriggerLog, metricData json.RawMessage, payload *cloudevent.CloudEvent[webhook.WebhookPayload]) error {
	cooldownPassed, err := checkCooldown(trigger, lastTrigger.LastTriggeredAt)
	if err != nil {
		return fmt.Errorf("failed to check cooldown: %w", err)
	}
	if !cooldownPassed {
		// If the cooldown period hasn't passed, skip the webhook
		return nil
	}

	err = m.sendWebhookNotification(ctx, trigger, payload)
	if err != nil {
		if richError, ok := richerrors.AsRichError(err); ok && richError.Code == webhookFailureCode {
			if err := m.handleWebhookFailure(ctx, trigger); err != nil {
				zerolog.Ctx(ctx).Error().Err(err).Msg("failed to handle webhook failure")
			}
		}
		return fmt.Errorf("failed to send webhook: %w", err)
	}

	if err := m.logWebhookTrigger(ctx, payload, metricData); err != nil {
		return fmt.Errorf("failed to log webhook trigger: %w", err)
	}
	if err := m.resetWebhookFailure(ctx, trigger); err != nil {
		return fmt.Errorf("failed to reset webhook failure: %w", err)
	}
	return nil
}

func checkCooldown(webhook *models.Trigger, lastTriggeredAt time.Time) (bool, error) {
	cooldown := webhook.CooldownPeriod
	if time.Since(lastTriggeredAt) >= time.Duration(cooldown)*time.Second {
		return true, nil
	}
	return false, nil
}

func (m *MetricListener) logWebhookTrigger(ctx context.Context, payload *cloudevent.CloudEvent[webhook.WebhookPayload], metricData json.RawMessage) error {
	now := time.Now()
	eventLog := &models.TriggerLog{
		ID:              payload.ID,
		TriggerID:       payload.Data.WebhookId,
		AssetDid:        payload.Data.AssetDID.String(),
		SnapshotData:    types.JSON(metricData),
		LastTriggeredAt: now,
		CreatedAt:       now,
	}
	if err := m.repo.CreateTriggerLog(ctx, eventLog); err != nil {
		return fmt.Errorf("failed to create trigger log: %w", err)
	}
	return nil
}

func (m *MetricListener) sendWebhookNotification(ctx context.Context, trigger *models.Trigger, payload *cloudevent.CloudEvent[webhook.WebhookPayload]) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal webhook payload: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, trigger.TargetURI, bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("failed to create webhook request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		return richerrors.Error{
			Code: webhookFailureCode,
			Err:  fmt.Errorf("failed to POST to webhook: %w", err),
		}
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return richerrors.Error{
			Code: webhookFailureCode,
			Err:  fmt.Errorf("webhook returned status code %d: %s", resp.StatusCode, string(respBody)),
		}
	}
	return nil
}

// createWebhookPayload creates a standardized webhook payload following industry best practices
func (m *MetricListener) createWebhookPayload(trigger *models.Trigger, assetDid cloudevent.ERC721DID) *cloudevent.CloudEvent[webhook.WebhookPayload] {

	payload := &cloudevent.CloudEvent[webhook.WebhookPayload]{
		CloudEventHeader: cloudevent.CloudEventHeader{
			ID:              uuid.New().String(),
			Source:          "vehicle-triggers-api", //TODO(kevin): Should be 0x of the storageNode
			Subject:         assetDid.String(),
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
			AssetDID:    assetDid,
			Condition:   trigger.Condition,
		},
	}
	return payload
}

func (m *MetricListener) handleWebhookFailure(ctx context.Context, trigger *models.Trigger) error {
	event, err := m.repo.GetTriggerByIDAndDeveloperLicense(ctx, trigger.ID, common.BytesToAddress(trigger.DeveloperLicenseAddress))
	if err != nil {
		return fmt.Errorf("could not fetch trigger: %w", err)
	}
	event.FailureCount += 1

	if event.FailureCount >= 5 {
		event.Status = triggersrepo.StatusFailed
		zerolog.Ctx(ctx).Info().Msgf("Webhook %s disabled due to excessive failures", trigger.ID)
	}
	if err := m.repo.UpdateTrigger(ctx, event); err != nil {
		return fmt.Errorf("failed to update event failure count: %w", err)
	}
	return nil
}

func (m *MetricListener) resetWebhookFailure(ctx context.Context, webhook *models.Trigger) error {
	event, err := m.repo.GetTriggerByIDAndDeveloperLicense(ctx, webhook.ID, common.BytesToAddress(webhook.DeveloperLicenseAddress))
	if err != nil {
		return fmt.Errorf("could not fetch event: %w", err)
	}
	if event.FailureCount == 0 {
		// if the failure count is 0, we don't need to reset it
		return nil
	}
	event.FailureCount = 0
	zerolog.Ctx(ctx).Debug().Msgf("Reset FailureCount for webhook %s", webhook.ID)
	if err := m.repo.UpdateTrigger(ctx, event); err != nil {
		return fmt.Errorf("failed to reset event failure count: %w", err)
	}
	return nil
}

func (m *MetricListener) getLastLogValue(ctx context.Context, triggerID string, assetDid cloudevent.ERC721DID) (*models.TriggerLog, error) {
	lastTrigger, err := m.repo.GetLastLogValue(ctx, triggerID, assetDid)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("failed to retrieve event logs: %w", err)
		}
		lastTrigger = &models.TriggerLog{
			SnapshotData: []byte("{}"),
			AssetDid:     assetDid.String(),
			TriggerID:    triggerID,
		}
	}
	return lastTrigger, nil
}

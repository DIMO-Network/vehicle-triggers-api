package nats

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/DIMO-Network/model-garage/pkg/vss"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// PublishSignals unpacks the multi-signal CloudEvent into one publish per
// signal name. Returns the count of successful publishes and any error
// encountered (first wins; remaining publishes still attempted).
//
// The subject is derived from the unpacked signal's name so consumers can
// filter on `dimo.signals.<name>` with low cardinality. Vehicle identity stays
// in the CloudEvent payload (the embedded `Subject` is the ERC721 DID).
func (c *Client) PublishSignals(ctx context.Context, ce vss.SignalCloudEvent) (int, error) {
	sigs := vss.UnpackSignals(ce)
	var firstErr error
	var ok int
	for _, sig := range sigs {
		// Re-wrap each signal in its own single-element CloudEvent so the
		// consumer side can keep using the same vss.UnpackSignals contract.
		single := vss.PackSignals(ce.CloudEventHeader, []vss.Signal{sig})
		payload, err := json.Marshal(single)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("marshal signal %q: %w", sig.Data.Name, err)
			}
			continue
		}
		if _, err := c.Publish(ctx, SignalSubject(sig.Data.Name), payload); err != nil {
			MetricsPublish(c.cfg.SignalsStream, "error")
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		MetricsPublish(c.cfg.SignalsStream, "ok")
		ok++
	}
	return ok, firstErr
}

// PublishEvents unpacks the multi-event CloudEvent and publishes one message
// per event keyed on the event name.
func (c *Client) PublishEvents(ctx context.Context, ce vss.EventCloudEvent) (int, error) {
	events := vss.UnpackEvents(ce)
	var firstErr error
	var ok int
	for _, ev := range events {
		single := vss.PackEvents(ce.CloudEventHeader, []vss.Event{ev})
		payload, err := json.Marshal(single)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("marshal event %q: %w", ev.Data.Name, err)
			}
			continue
		}
		if _, err := c.Publish(ctx, EventSubject(ev.Data.Name), payload); err != nil {
			MetricsPublish(c.cfg.EventsStream, "error")
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		MetricsPublish(c.cfg.EventsStream, "ok")
		ok++
	}
	return ok, firstErr
}

// publishDLQ writes a poison message to the DLQ stream, preserving the
// original subject hierarchy under dimo.dlq.* and stamping headers with
// triage context: subject, source name, vehicle DID, failure reason, deliver
// count, original stream, and the timestamp the failure was recorded.
// Best-effort: returns an error if the publish fails so the caller can fall
// back to nak.
//
// We intentionally do NOT include a developer-license header because one
// inbound message may match webhooks across multiple developers; tagging
// "the" developer would be misleading. Triage tools look up impacted
// developers by joining the asset DID against the trigger registry.
func (c *Client) publishDLQ(m jetstream.Msg, handlerErr error) error {
	meta, _ := m.Metadata()
	headers := nats.Header{}
	for k, vs := range m.Headers() {
		headers[k] = vs
	}
	headers.Set("X-Original-Subject", m.Subject())
	headers.Set("X-Failure-Reason", handlerErr.Error())
	headers.Set("X-Recorded-At", time.Now().UTC().Format(time.RFC3339Nano))
	if name := sourceNameFromSubject(m.Subject()); name != "" {
		headers.Set("X-Source-Name", name)
	}
	if did := extractAssetDID(m.Data()); did != "" {
		headers.Set("X-Asset-DID", did)
	}
	if meta != nil {
		headers.Set("X-Original-Stream", meta.Stream)
		headers.Set("X-Delivered-Count", fmt.Sprintf("%d", meta.NumDelivered))
	}
	dlq := &nats.Msg{
		Subject: DLQSubject(m.Subject()),
		Data:    m.Data(),
		Header:  headers,
	}
	if _, err := c.JS.PublishMsg(context.Background(), dlq); err != nil {
		MetricsPublish(c.cfg.DLQStream, "error")
		return fmt.Errorf("dlq publish %q: %w", dlq.Subject, err)
	}
	MetricsPublish(c.cfg.DLQStream, "ok")
	return nil
}

// sourceNameFromSubject returns the trailing token after the dimo.signals.
// or dimo.events. prefix. Used to populate the X-Source-Name header on DLQ
// records so ops can group by signal/event name without parsing the subject.
func sourceNameFromSubject(subject string) string {
	for _, prefix := range []string{SignalSubjectPrefix + ".", EventSubjectPrefix + "."} {
		if strings.HasPrefix(subject, prefix) {
			return subject[len(prefix):]
		}
	}
	return ""
}

// extractAssetDID does a best-effort parse of the payload to lift the
// CloudEvent subject (== ERC721 DID) out for the DLQ header. We use a
// minimal struct so we don't pay the full vss decode cost for messages that
// are already known broken.
func extractAssetDID(body []byte) string {
	var envelope struct {
		Subject string `json:"subject"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ""
	}
	return envelope.Subject
}

// PublishTriggerFired writes a per-fire audit record to the audit stream
// keyed on developer license. Uses async publish so a stalled audit stream
// can't backpressure the evaluation path; the in-flight ceiling is governed
// by NATS_PUBLISH_ASYNC_MAX_PENDING.
func (c *Client) PublishTriggerFired(ctx context.Context, devLicense string, record []byte) error {
	subject := AuditSubject(devLicense)
	if _, err := c.JS.PublishAsync(subject, record); err != nil {
		MetricsPublish(c.cfg.AuditStream, "error")
		return fmt.Errorf("audit publish %q: %w", subject, err)
	}
	MetricsPublish(c.cfg.AuditStream, "ok")
	return nil
}

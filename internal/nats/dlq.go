package nats

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// dlqSubjectFor derives the DLQ publish subject from the configured DLQ
// subject filter. We trim the trailing wildcard (`.>` or `.*`) and append
// the original subject so DLQ entries keep their full source hierarchy and
// filter by original signal/event name. Falls back to the package-level
// DLQSubject (dimo.dlq.*) when no DLQ subject is configured.
func (c *Client) dlqSubjectFor(original string) string {
	prefix := strings.TrimSuffix(c.cfg.DLQSubject, ".>")
	prefix = strings.TrimSuffix(prefix, ".*")
	if prefix == "" || prefix == c.cfg.DLQSubject {
		// No wildcard suffix or empty config — fall back to package default
		// to avoid publishing to a subject the DLQ stream doesn't capture.
		return DLQSubject(original)
	}
	return prefix + "." + original
}

// publishDLQ writes a poison message to the DLQ stream, preserving the
// original subject hierarchy under the configured DLQ prefix and stamping
// headers with triage context: subject, source name, vehicle DID, failure
// reason, deliver count, original stream, and the timestamp the failure was
// recorded.
//
// Best-effort: returns an error if the publish fails so the caller (PullLoop)
// can fall back to nak instead of Term.
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
		Subject: c.dlqSubjectFor(m.Subject()),
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

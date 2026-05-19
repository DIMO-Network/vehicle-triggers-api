package nats

import (
	"fmt"
	"strings"
)

// Subject design notes
//
// Stream subject cardinality scales with the number of signal/event NAMES, not
// the number of vehicles. With ~30 signals and a handful of event types this
// keeps total stream-subject space in the low hundreds even with millions of
// vehicles. The vehicle identity travels in the CloudEvent payload
// (`signalCE.Subject` is the ERC721 DID), so consumers parse it from the body
// rather than from the subject. This is the deliberate trade: pay one JSON
// decode per message in exchange for keeping JetStream's per-subject state
// tiny.
//
// The audit subject keys on developer license so per-developer billing
// aggregation can use subject-level consumer filters; trigger ID lives in the
// payload.

const (
	SignalSubjectPrefix = "dimo.signals"
	EventSubjectPrefix  = "dimo.events"
	AuditSubjectPrefix  = "dimo.trigger.fired"
)

// SignalSubject builds the publish subject for a signal.
// Shape: dimo.signals.<signalName>
func SignalSubject(signalName string) string {
	return fmt.Sprintf("%s.%s", SignalSubjectPrefix, sanitize(signalName))
}

// EventSubject builds the publish subject for an event.
// Shape: dimo.events.<eventName>
func EventSubject(eventName string) string {
	return fmt.Sprintf("%s.%s", EventSubjectPrefix, sanitize(eventName))
}

// AuditSubject builds the publish subject for a trigger-fired audit event.
// Shape: dimo.trigger.fired.<developerLicense>
func AuditSubject(developerLicense string) string {
	return fmt.Sprintf("%s.%s", AuditSubjectPrefix, sanitize(developerLicense))
}

// SignalFilter returns the consumer filter subject for one signal name.
// Shape: dimo.signals.<signalName>
func SignalFilter(signalName string) string {
	return SignalSubject(signalName)
}

// EventFilter returns the consumer filter subject for one event name.
// Shape: dimo.events.<eventName>
func EventFilter(eventName string) string {
	return EventSubject(eventName)
}

// AllSignalsFilter returns the wildcard filter matching every signal subject.
func AllSignalsFilter() string { return SignalSubjectPrefix + ".>" }

// AllEventsFilter returns the wildcard filter matching every event subject.
func AllEventsFilter() string { return EventSubjectPrefix + ".>" }

// sanitize replaces characters illegal in NATS subject tokens with underscores
// and substitutes a placeholder for empty input so we never emit adjacent-dot
// subjects like "dimo.signals..speed".
// NATS tokens disallow spaces, dots (except as separators), *, >, and control chars.
func sanitize(s string) string {
	if s == "" {
		return "_"
	}
	r := strings.NewReplacer(
		" ", "_",
		".", "_",
		"*", "_",
		">", "_",
		"\t", "_",
		"\n", "_",
		"\r", "_",
	)
	return r.Replace(s)
}

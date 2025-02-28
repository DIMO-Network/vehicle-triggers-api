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

type CloudEvent[T any] struct {
	ID          string    `json:"id"`
	Source      string    `json:"source"`
	Subject     string    `json:"subject"`
	SpecVersion string    `json:"specversion"`
	Time        time.Time `json:"time"`
	Type        string    `json:"type"`
	Data        T         `json:"data"`
}

type VehicleEventData struct {
	VehicleID string `json:"vehicleId"`
	Odometer  int    `json:"odometer"`
	// More fields later
}

type VehicleEventListener struct {
	log zerolog.Logger
}

func NewVehicleEventListener(logger zerolog.Logger) *VehicleEventListener {
	return &VehicleEventListener{
		log: logger,
	}
}

func (l *VehicleEventListener) ProcessVehicleEvents(messages <-chan *message.Message) {
	for msg := range messages {
		if err := l.processMessage(msg); err != nil {
			l.log.Err(err).Msg("error processing vehicle event message")
		}
		msg.Ack()
	}
}

func (l *VehicleEventListener) processMessage(msg *message.Message) error {
	l.log.Info().
		Str("message_id", msg.UUID).
		Msgf("Received payload: %s", string(msg.Payload))

	var evt CloudEvent[VehicleEventData]
	if err := json.Unmarshal(msg.Payload, &evt); err != nil {
		return errors.Wrap(err, "failed to parse vehicle event JSON")
	}

	l.log.Info().
		Str("cloud_event_id", evt.ID).
		Str("vehicle_id", evt.Data.VehicleID).
		Int("odometer", evt.Data.Odometer).
		Msg("Parsed VehicleEventData")

	shouldTrigger, err := l.evaluateCEL(evt.Data.Odometer)
	if err != nil {
		return errors.Wrap(err, "CEL evaluation failed")
	}
	if shouldTrigger {
		l.log.Info().Msg("CEL condition met (odometer > 100): Webhook should be fired")
		// Simulate a webhook call to a local endpoint.
		if err := l.sendWebhookNotification(evt); err != nil {
			l.log.Err(err).Msg("failed to send webhook notification")
		}
	} else {
		l.log.Info().Msg("CEL condition not met: No webhook triggered")
	}

	return nil
}

// evaluateCEL evaluates the expression "odometer > 100" using CEL.
func (l *VehicleEventListener) evaluateCEL(odometer int) (bool, error) {
	env, err := cel.NewEnv(
		cel.Variable("odometer", cel.IntType),
	)

	if err != nil {
		return false, errors.Wrap(err, "failed to create CEL environment")
	}

	ast, issues := env.Compile("odometer > 100")
	if issues != nil && issues.Err() != nil {
		return false, errors.Wrap(issues.Err(), "failed to compile CEL expression")
	}

	prg, err := env.Program(ast)
	if err != nil {
		return false, errors.Wrap(err, "failed to create CEL program")
	}

	out, _, err := prg.Eval(map[string]interface{}{"odometer": odometer})
	if err != nil {
		return false, errors.Wrap(err, "failed to evaluate CEL program")
	}

	if out == types.True {
		return true, nil
	}
	return false, nil
}

func (l *VehicleEventListener) sendWebhookNotification(evt CloudEvent[VehicleEventData]) error {
	url := "http://localhost:8081/webhook"
	payload, err := json.Marshal(evt)
	if err != nil {
		return errors.Wrap(err, "failed to marshal webhook payload")
	}
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(payload))
	if err != nil {
		return errors.Wrap(err, "failed to send POST request to webhook endpoint")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return errors.Errorf("webhook endpoint returned status code %d", resp.StatusCode)
	}
	l.log.Info().Msg("Webhook notification sent successfully")
	return nil
}

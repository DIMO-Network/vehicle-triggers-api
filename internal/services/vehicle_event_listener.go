package services

import (
	"encoding/json"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
)

// Refine later
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
	// TODO: Add more fields (e.g., event type, location, etc.) once finalized
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
		Msg("Parsed VehicleEventData")

	// TODO: ??

	return nil
}

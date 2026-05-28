package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func validBaseSettings() Settings {
	return Settings{
		KafkaBrokers:       "kafka:9092",
		DeviceSignalsTopic: "signals",
		DeviceEventsTopic:  "events",
		MaxInFlight:        50,
		NATS: NATSSettings{
			Mode:          "off",
			URL:           "nats://localhost:4222",
			MaxDeliver:    5,
			MaxAckPending: 5000,
			AckWait:       45 * time.Second,
			FetchBatch:    100,
			StreamReplicas: 1,
			SignalsMaxAge: 24 * time.Hour,
			EventsMaxAge:  24 * time.Hour,
		},
	}
}

func TestSettingsValidate(t *testing.T) {
	t.Parallel()

	t.Run("happy path off", func(t *testing.T) {
		require.NoError(t, validBaseSettings().Validate())
	})

	t.Run("happy path exclusive omits kafka", func(t *testing.T) {
		s := validBaseSettings()
		s.NATS.Mode = "exclusive"
		s.KafkaBrokers = ""
		s.DeviceSignalsTopic = ""
		s.DeviceEventsTopic = ""
		require.NoError(t, s.Validate())
	})

	t.Run("rejects unknown mode", func(t *testing.T) {
		s := validBaseSettings()
		s.NATS.Mode = "bogus"
		require.ErrorContains(t, s.Validate(), "NATS_MODE")
	})

	t.Run("rejects missing kafka outside exclusive", func(t *testing.T) {
		s := validBaseSettings()
		s.KafkaBrokers = ""
		require.ErrorContains(t, s.Validate(), "KAFKA_BROKERS")
	})

	t.Run("rejects MaxDeliver < 1 when nats enabled", func(t *testing.T) {
		s := validBaseSettings()
		s.NATS.Mode = "primary"
		s.NATS.MaxDeliver = 0
		require.ErrorContains(t, s.Validate(), "NATS_MAX_DELIVER")
	})

	t.Run("flags max-age shorter than retry window", func(t *testing.T) {
		s := validBaseSettings()
		s.NATS.Mode = "primary"
		s.NATS.AckWait = 60 * time.Second
		s.NATS.MaxDeliver = 5
		s.NATS.SignalsMaxAge = time.Minute // 1m < 60s * 5 = 5m
		require.ErrorContains(t, s.Validate(), "SIGNALS_MAX_AGE")
	})

	t.Run("flags MaxInFlight 0", func(t *testing.T) {
		s := validBaseSettings()
		s.MaxInFlight = 0
		require.ErrorContains(t, s.Validate(), "MAX_IN_FLIGHT")
	})
}

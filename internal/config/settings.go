package config

import (
	"github.com/DIMO-Network/shared/db"
)

// Settings contains the application config
type Settings struct {
	Environment        string `yaml:"ENVIRONMENT"`
	Port               string `yaml:"PORT"`
	GRPCPort           string `yaml:"GRPC_PORT"`
	LogLevel           string `yaml:"LOG_LEVEL"`
	ServiceName        string `yaml:"SERVICE_NAME"`
	IdentityAPIURL     string `yaml:"IdentityAPIURL"`
	KafkaBrokers       string `yaml:"KAFKA_BROKERS"`
	VehicleEventsTopic string `yaml:"VEHICLE_EVENTS_TOPIC"`

	DB db.Settings `yaml:"DB"`
}

func (s *Settings) IsProduction() bool {
	return s.Environment == "prod" // this string is set in the helm chart values-prod.yaml
}

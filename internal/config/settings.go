package config

import (
	"github.com/DIMO-Network/shared/pkg/db"
)

// Settings contains the application config
type Settings struct {
	Environment        string `yaml:"ENVIRONMENT"`
	Port               string `yaml:"PORT"`
	GRPCPort           string `yaml:"GRPC_PORT"`
	LogLevel           string `yaml:"LOG_LEVEL"`
	ServiceName        string `yaml:"SERVICE_NAME"`
	IdentityAPIURL     string `yaml:"IDENTITY_API_URL"`
	KafkaBrokers       string `yaml:"KAFKA_BROKERS"`
	DeviceSignalsTopic string `yaml:"DEVICE_SIGNALS_TOPIC"`
	MonitoringPort     string `yaml:"MONITORING_PORT"`

	DB db.Settings `yaml:"DB"`
}

func (s *Settings) IsProduction() bool {
	return s.Environment == "prod" // this string is set in the helm chart values-prod.yaml
}

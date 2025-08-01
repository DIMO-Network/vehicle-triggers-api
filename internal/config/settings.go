package config

import (
	"github.com/DIMO-Network/shared/pkg/db"
)

// Settings contains the application config
type Settings struct {
	Port               int    `env:"PORT"`
	MonPort            int    `env:"MON_PORT"`
	EnablePprof        bool   `env:"ENABLE_PPROF"`
	LogLevel           string `env:"LOG_LEVEL"`
	ServiceName        string `env:"SERVICE_NAME"`
	IdentityAPIURL     string `env:"IDENTITY_API_URL"`
	KafkaBrokers       string `env:"KAFKA_BROKERS"`
	DeviceSignalsTopic string `env:"DEVICE_SIGNALS_TOPIC"`

	DB db.Settings `envPrefix:"DB_"`
}

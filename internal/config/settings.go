package config

import (
	"github.com/DIMO-Network/shared/pkg/db"
	"github.com/ethereum/go-ethereum/common"
)

// Settings contains the application config
type Settings struct {
	Port                  int            `env:"PORT"`
	MonPort               int            `env:"MON_PORT"`
	EnablePprof           bool           `env:"ENABLE_PPROF"`
	LogLevel              string         `env:"LOG_LEVEL"`
	ServiceName           string         `env:"SERVICE_NAME"`
	IdentityAPIURL        string         `env:"IDENTITY_API_URL"`
	KafkaBrokers          string         `env:"KAFKA_BROKERS"`
	DeviceSignalsTopic    string         `env:"DEVICE_SIGNALS_TOPIC"`
	TokenExchangeGRPCAddr string         `env:"TOKEN_EXCHANGE_GRPC_ADDR"`
	VehicleNFTAddress     common.Address `env:"VEHICLE_NFT_ADDRESS"`
	DIMORegistryChainID   uint64         `env:"DIMO_REGISTRY_CHAIN_ID"`

	DB db.Settings `envPrefix:"DB_"`
}

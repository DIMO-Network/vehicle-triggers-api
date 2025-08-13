package config

import (
	"time"

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
	JWKKeySetURL          string         `env:"JWK_KEY_SET_URL"`
	IdentityAPIURL        string         `env:"IDENTITY_API_URL"`
	KafkaBrokers          string         `env:"KAFKA_BROKERS"`
	DeviceSignalsTopic    string         `env:"DEVICE_SIGNALS_TOPIC"`
	DeviceEventsTopic     string         `env:"DEVICE_EVENTS_TOPIC"`
	TokenExchangeGRPCAddr string         `env:"TOKEN_EXCHANGE_GRPC_ADDR"`
	VehicleNFTAddress     common.Address `env:"VEHICLE_NFT_ADDRESS"`
	DIMORegistryChainID   uint64         `env:"DIMO_REGISTRY_CHAIN_ID"`
	// CacheDebounceTime wait time betweeen to successive cache refreshes
	CacheDebounceTime time.Duration `env:"CACHE_DEBOUNCE_TIME"`

	DB db.Settings `envPrefix:"DB_"`
}

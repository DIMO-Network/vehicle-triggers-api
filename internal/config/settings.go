package config

import (
	"time"

	"github.com/DIMO-Network/shared/pkg/db"
	"github.com/ethereum/go-ethereum/common"
)

// Settings contains the application config
type Settings struct {
	Port                              int            `env:"PORT"`
	MonPort                           int            `env:"MON_PORT"`
	EnablePprof                       bool           `env:"ENABLE_PPROF"`
	LogLevel                          string         `env:"LOG_LEVEL"`
	ServiceName                       string         `env:"SERVICE_NAME"`
	JWKKeySetURL                      string         `env:"JWK_KEY_SET_URL"`
	IdentityAPIURL                    string         `env:"IDENTITY_API_URL"`
	KafkaBrokers                      string         `env:"KAFKA_BROKERS"`
	DeviceSignalsTopic                string         `env:"DEVICE_SIGNALS_TOPIC"`
	DeviceEventsTopic                 string         `env:"DEVICE_EVENTS_TOPIC"`
	TokenExchangeGRPCAddr             string         `env:"TOKEN_EXCHANGE_GRPC_ADDR"`
	TokenExchangeCacheExpiration      time.Duration  `env:"TOKEN_EXCHANGE_CACHE_EXPIRATION"`
	TokenExchangeCacheCleanupInterval time.Duration  `env:"TOKEN_EXCHANGE_CACHE_CLEANUP_INTERVAL"`
	VehicleNFTAddress                 common.Address `env:"VEHICLE_NFT_ADDRESS"`
	DIMORegistryChainID               uint64         `env:"DIMO_REGISTRY_CHAIN_ID"`
	MaxWebhookFailureCount            uint           `env:"MAX_WEBHOOK_FAILURE_COUNT"`
	// MaxInFlight is the maximum number of messages to process concurrently per consumer
	MaxInFlight int `env:"MAX_IN_FLIGHT" envDefault:"50"`
	// CacheDebounceTime wait time betweeen to successive cache refreshes
	CacheDebounceTime time.Duration `env:"CACHE_DEBOUNCE_TIME"`
	// CacheBuildWorkers caps the parallelism of the per-trigger fetch+CEL
	// compile loop in webhookcache.PopulateCache. Each worker takes one DB
	// roundtrip plus one CEL compile at a time, so it doubles as a DB
	// connection-pool guard. Defaults to 2 because the prod pod is pinned
	// to ~1 CPU; raise it on multi-core nodes.
	CacheBuildWorkers int `env:"CACHE_BUILD_WORKERS" envDefault:"2"`

	DB db.Settings `envPrefix:"DB_"`
}

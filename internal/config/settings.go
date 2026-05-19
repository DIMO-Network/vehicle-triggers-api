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

	NATS NATSSettings `envPrefix:"NATS_"`

	DB db.Settings `envPrefix:"DB_"`
}

// NATSSettings holds NATS JetStream wiring.
//
// Mode controls ingest topology:
//   - off     (default): Kafka consumers evaluate triggers, NATS unused.
//   - primary:           Kafka consumers parse + republish to NATS without
//                        evaluating; NATS consumers do the evaluation and
//                        webhook dispatch. This lets DIS keep producing to
//                        Kafka while the service runs entirely on NATS
//                        internally, avoiding double-fires.
//
// When Mode != off the service refuses to start if a NATS connection cannot
// be established.
type NATSSettings struct {
	Mode string `env:"MODE" envDefault:"off"`

	URL       string `env:"URL" envDefault:"nats://localhost:4222"`
	CredsFile string `env:"CREDS_FILE"`
	Name      string `env:"NAME" envDefault:"vehicle-triggers-api"`

	SignalsStream  string `env:"SIGNALS_STREAM" envDefault:"DIMO_SIGNALS"`
	EventsStream   string `env:"EVENTS_STREAM" envDefault:"DIMO_EVENTS"`
	AuditStream    string `env:"AUDIT_STREAM" envDefault:"DIMO_TRIGGER_AUDIT"`
	SignalsSubject string `env:"SIGNALS_SUBJECT" envDefault:"dimo.signals.>"`
	EventsSubject  string `env:"EVENTS_SUBJECT" envDefault:"dimo.events.>"`
	AuditSubject   string `env:"AUDIT_SUBJECT" envDefault:"dimo.trigger.fired.>"`

	SignalsDurable string `env:"SIGNALS_DURABLE" envDefault:"triggers-signals"`
	EventsDurable  string `env:"EVENTS_DURABLE" envDefault:"triggers-events"`

	StreamReplicas         int           `env:"STREAM_REPLICAS" envDefault:"1"`
	SignalsMaxAge          time.Duration `env:"SIGNALS_MAX_AGE" envDefault:"24h"`
	EventsMaxAge           time.Duration `env:"EVENTS_MAX_AGE" envDefault:"24h"`
	AuditMaxAge            time.Duration `env:"AUDIT_MAX_AGE" envDefault:"2160h"` // 90d
	FetchBatch             int           `env:"FETCH_BATCH" envDefault:"100"`
	AckWait                time.Duration `env:"ACK_WAIT" envDefault:"45s"`
	MaxDeliver             int           `env:"MAX_DELIVER" envDefault:"5"`
	MaxAckPending          int           `env:"MAX_ACK_PENDING" envDefault:"5000"`
	FilterSubjectCap       int           `env:"FILTER_SUBJECT_CAP" envDefault:"2048"`
	PublishAsyncMaxPending int           `env:"PUBLISH_ASYNC_MAX_PENDING" envDefault:"4000"`

	WebhooksBucket     string        `env:"WEBHOOKS_BUCKET" envDefault:"webhooks"`
	SignalIndexBucket  string        `env:"SIGNAL_INDEX_BUCKET" envDefault:"signal_index"`
	TriggerStateBucket string        `env:"TRIGGER_STATE_BUCKET" envDefault:"trigger_state"`
	TriggerStateTTL    time.Duration `env:"TRIGGER_STATE_TTL" envDefault:"168h"` // 7d

	SignalIndexDebounce time.Duration `env:"SIGNAL_INDEX_DEBOUNCE" envDefault:"5s"`
}

// Enabled reports whether any NATS wiring should run.
func (n NATSSettings) Enabled() bool { return n.Mode != "" && n.Mode != "off" }

// PrimaryMode reports whether NATS owns the evaluation path (Kafka becomes a
// bridge that only republishes parsed payloads to NATS).
func (n NATSSettings) PrimaryMode() bool { return n.Mode == "primary" }

package config

import (
	"errors"
	"fmt"
	"strings"
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

	// MaxAllowedCooldownPeriod is the hard upper bound (seconds) on
	// triggers.cooldown_period. It exists so the cooldown KV bucket TTL is
	// guaranteed to outlive any configured cooldown - silent fires
	// otherwise once TTL expires the state record mid-cooldown. Default
	// 30 days; service refuses to start unless TriggerStateTTL is at least
	// 2x this value (see Settings.Validate).
	MaxAllowedCooldownPeriod int `env:"MAX_ALLOWED_COOLDOWN_PERIOD" envDefault:"2592000"`

	// SigningSecretKeyHex is a 64-char hex (32-byte) key used to AES-256-GCM
	// encrypt per-trigger HMAC signing secrets at rest in Postgres. Empty
	// disables encryption (legacy plaintext storage). Rows written before
	// the key is enabled remain readable - the cipher falls back to
	// plaintext when the stored value doesn't look encrypted.
	SigningSecretKeyHex string `env:"SIGNING_SECRET_KEY_HEX"`

	NATS NATSSettings `envPrefix:"NATS_"`

	DB db.Settings `envPrefix:"DB_"`
}

// NATSSettings holds NATS JetStream wiring.
//
// Mode controls ingest topology:
//   - off       (default): Kafka consumers evaluate triggers, NATS unused.
//   - primary:             Kafka consumers parse + republish to NATS without
//                          evaluating; NATS consumers do the evaluation and
//                          webhook dispatch. Transitional mode used while DIS
//                          still publishes to Kafka but the service runs
//                          evaluation on NATS, avoiding double-fires.
//   - exclusive:           NATS only. Kafka consumers are not created, and
//                          KAFKA_* env vars become optional. Target state
//                          once DIS publishes directly to JetStream.
//
// When Mode != off the service refuses to start if a NATS connection cannot
// be established.
type NATSSettings struct {
	Mode string `env:"MODE" envDefault:"off"`

	URL       string `env:"URL" envDefault:"nats://localhost:4222"`
	CredsFile string `env:"CREDS_FILE"`
	Name      string `env:"NAME" envDefault:"vehicle-triggers-api"`

	SignalsStream     string `env:"SIGNALS_STREAM" envDefault:"DIMO_SIGNALS"`
	EventsStream      string `env:"EVENTS_STREAM" envDefault:"DIMO_EVENTS"`
	AuditStream       string `env:"AUDIT_STREAM" envDefault:"DIMO_TRIGGER_AUDIT"`
	DLQStream         string `env:"DLQ_STREAM" envDefault:"DIMO_TRIGGER_DLQ"`
	ConfigAuditStream string `env:"CONFIG_AUDIT_STREAM" envDefault:"DIMO_CONFIG_AUDIT"`
	SignalsSubject    string `env:"SIGNALS_SUBJECT" envDefault:"dimo.signals.>"`
	EventsSubject     string `env:"EVENTS_SUBJECT" envDefault:"dimo.events.>"`
	AuditSubject      string `env:"AUDIT_SUBJECT" envDefault:"dimo.trigger.fired.>"`
	DLQSubject        string `env:"DLQ_SUBJECT" envDefault:"dimo.dlq.>"`
	ConfigAuditSubject string `env:"CONFIG_AUDIT_SUBJECT" envDefault:"dimo.config.changed.>"`

	SignalsDurable string `env:"SIGNALS_DURABLE" envDefault:"triggers-signals"`
	EventsDurable  string `env:"EVENTS_DURABLE" envDefault:"triggers-events"`

	StreamReplicas         int           `env:"STREAM_REPLICAS" envDefault:"1"`
	SignalsMaxAge          time.Duration `env:"SIGNALS_MAX_AGE" envDefault:"24h"`
	EventsMaxAge           time.Duration `env:"EVENTS_MAX_AGE" envDefault:"24h"`
	AuditMaxAge            time.Duration `env:"AUDIT_MAX_AGE" envDefault:"2160h"`        // 90d
	DLQMaxAge              time.Duration `env:"DLQ_MAX_AGE" envDefault:"168h"`           // 7d
	ConfigAuditMaxAge      time.Duration `env:"CONFIG_AUDIT_MAX_AGE" envDefault:"2160h"` // 90d
	// Per-stream hard storage caps. 0 = unlimited (rely on MaxAge). Pair
	// with Discard:DiscardOld so the oldest data is evicted when the cap
	// is hit. Defaults sized for the prod load envelope; raise per
	// SCALING.md sizing math if disk usage tells you to.
	SignalsMaxBytes     int64 `env:"SIGNALS_MAX_BYTES" envDefault:"107374182400"`     // 100 GiB
	EventsMaxBytes      int64 `env:"EVENTS_MAX_BYTES" envDefault:"10737418240"`       // 10 GiB
	AuditMaxBytes       int64 `env:"AUDIT_MAX_BYTES" envDefault:"107374182400"`       // 100 GiB
	DLQMaxBytes         int64 `env:"DLQ_MAX_BYTES" envDefault:"10737418240"`          // 10 GiB
	ConfigAuditMaxBytes int64 `env:"CONFIG_AUDIT_MAX_BYTES" envDefault:"1073741824"` // 1 GiB
	FetchBatch             int           `env:"FETCH_BATCH" envDefault:"100"`
	AckWait                time.Duration `env:"ACK_WAIT" envDefault:"45s"`
	MaxDeliver             int           `env:"MAX_DELIVER" envDefault:"5"`
	MaxAckPending          int           `env:"MAX_ACK_PENDING" envDefault:"5000"`
	FilterSubjectCap       int           `env:"FILTER_SUBJECT_CAP" envDefault:"2048"`
	PublishAsyncMaxPending int           `env:"PUBLISH_ASYNC_MAX_PENDING" envDefault:"4000"`

	// Dispatcher decouples webhook delivery from the JetStream handler.
	// Workers=0 keeps the legacy synchronous behavior; >0 spins up a worker
	// pool that owns delivery + state + audit + failure-count bookkeeping
	// so a slow receiver can't throttle the consumer.
	DispatcherWorkers           int           `env:"DISPATCHER_WORKERS" envDefault:"32"`
	DispatcherQueueSize         int           `env:"DISPATCHER_QUEUE_SIZE" envDefault:"4096"`
	DispatcherRetryAttempts     int           `env:"DISPATCHER_RETRY_ATTEMPTS" envDefault:"2"`
	DispatcherRetryInitialDelay time.Duration `env:"DISPATCHER_RETRY_INITIAL_DELAY" envDefault:"100ms"`
	DispatcherPerHostRPS        float64       `env:"DISPATCHER_PER_HOST_RPS" envDefault:"0"`
	DispatcherPerHostBurst      int           `env:"DISPATCHER_PER_HOST_BURST" envDefault:"0"`

	// Audit queue is fronted by a fire-and-forget pool so the dispatcher's
	// success path never spawns one goroutine per fire (was a goroutine
	// explosion risk at 30k/s when the audit publisher slowed down).
	AuditWorkers   int `env:"AUDIT_WORKERS" envDefault:"4"`
	AuditQueueSize int `env:"AUDIT_QUEUE_SIZE" envDefault:"16384"`

	TriggerStateBucket   string        `env:"TRIGGER_STATE_BUCKET" envDefault:"trigger_state"`
	TriggerStateTTL      time.Duration `env:"TRIGGER_STATE_TTL" envDefault:"168h"` // 7d
	SignalHistoryBucket  string        `env:"SIGNAL_HISTORY_BUCKET" envDefault:"signal_history"`
	SignalHistoryTTL     time.Duration `env:"SIGNAL_HISTORY_TTL" envDefault:"168h"` // 7d
}

// Enabled reports whether any NATS wiring should run.
func (n NATSSettings) Enabled() bool { return n.Mode != "" && n.Mode != "off" }

// PrimaryMode reports whether NATS owns the evaluation path. True for both
// "primary" (Kafka bridges into NATS) and "exclusive" (no Kafka at all).
func (n NATSSettings) PrimaryMode() bool { return n.Mode == "primary" || n.Mode == "exclusive" }

// KafkaDisabled reports whether the Kafka consumers should be skipped
// entirely. True only in exclusive mode.
func (n NATSSettings) KafkaDisabled() bool { return n.Mode == "exclusive" }

// Validate returns an error describing any misconfiguration. Called from
// main at startup so we fail fast rather than discovering trouble at first
// signal. Sweeps the cross-field constraints that env tags can't express:
// mode enum, NATS URL when enabled, Kafka brokers when Kafka is in play,
// and the retry/backoff/retention math.
func (s Settings) Validate() error {
	var errs []string

	if err := s.NATS.Validate(); err != nil {
		errs = append(errs, err.Error())
	}

	// Kafka is required unless NATS is in exclusive mode.
	if !s.NATS.KafkaDisabled() {
		if strings.TrimSpace(s.KafkaBrokers) == "" {
			errs = append(errs, "KAFKA_BROKERS empty but NATS_MODE != exclusive")
		}
		if strings.TrimSpace(s.DeviceSignalsTopic) == "" {
			errs = append(errs, "DEVICE_SIGNALS_TOPIC empty but NATS_MODE != exclusive")
		}
		if strings.TrimSpace(s.DeviceEventsTopic) == "" {
			errs = append(errs, "DEVICE_EVENTS_TOPIC empty but NATS_MODE != exclusive")
		}
	}

	if s.MaxInFlight < 1 {
		errs = append(errs, fmt.Sprintf("MAX_IN_FLIGHT=%d must be >= 1", s.MaxInFlight))
	}

	if s.MaxAllowedCooldownPeriod < 1 {
		errs = append(errs, fmt.Sprintf("MAX_ALLOWED_COOLDOWN_PERIOD=%d must be >= 1", s.MaxAllowedCooldownPeriod))
	}

	// Distributed-cooldown invariant: the trigger_state KV bucket TTL must
	// outlive the longest cooldown by a comfortable factor. Otherwise a
	// long-cooldown trigger silently re-fires once TTL expires mid-window.
	// Only enforced when NATS is wired in (the in-memory fallback isn't
	// TTL-bounded).
	if s.NATS.Enabled() && s.MaxAllowedCooldownPeriod > 0 {
		maxCooldown := time.Duration(s.MaxAllowedCooldownPeriod) * time.Second
		minTTL := 2 * maxCooldown
		if s.NATS.TriggerStateTTL < minTTL {
			errs = append(errs, fmt.Sprintf(
				"NATS_TRIGGER_STATE_TTL=%s must be >= 2*MAX_ALLOWED_COOLDOWN_PERIOD (=%s); raise the TTL or lower the cooldown ceiling",
				s.NATS.TriggerStateTTL, minTTL,
			))
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return errors.New("invalid configuration: " + strings.Join(errs, "; "))
}

// Validate checks the NATS configuration's internal consistency.
func (n NATSSettings) Validate() error {
	var errs []string

	switch n.Mode {
	case "", "off", "primary", "exclusive":
	default:
		errs = append(errs, fmt.Sprintf("NATS_MODE=%q must be one of off|primary|exclusive", n.Mode))
	}

	if !n.Enabled() {
		// Nothing else matters until NATS is turned on.
		if len(errs) == 0 {
			return nil
		}
		return errors.New(strings.Join(errs, "; "))
	}

	if strings.TrimSpace(n.URL) == "" {
		errs = append(errs, "NATS_URL empty but mode != off")
	}
	if n.MaxDeliver < 1 {
		errs = append(errs, fmt.Sprintf("NATS_MAX_DELIVER=%d must be >= 1", n.MaxDeliver))
	}
	if n.MaxAckPending < 1 {
		errs = append(errs, fmt.Sprintf("NATS_MAX_ACK_PENDING=%d must be >= 1", n.MaxAckPending))
	}
	if n.AckWait <= 0 {
		errs = append(errs, fmt.Sprintf("NATS_ACK_WAIT=%s must be > 0", n.AckWait))
	}
	if n.FetchBatch < 1 {
		errs = append(errs, fmt.Sprintf("NATS_FETCH_BATCH=%d must be >= 1", n.FetchBatch))
	}
	if n.StreamReplicas < 1 {
		errs = append(errs, fmt.Sprintf("NATS_STREAM_REPLICAS=%d must be >= 1", n.StreamReplicas))
	}

	// Retention must outlive the worst-case retry window: AckWait * MaxDeliver.
	// Otherwise messages can be discarded mid-retry.
	worstCase := n.AckWait * time.Duration(n.MaxDeliver)
	if n.SignalsMaxAge > 0 && n.SignalsMaxAge < worstCase {
		errs = append(errs, fmt.Sprintf("NATS_SIGNALS_MAX_AGE=%s shorter than AckWait*MaxDeliver=%s; messages may be discarded mid-retry", n.SignalsMaxAge, worstCase))
	}
	if n.EventsMaxAge > 0 && n.EventsMaxAge < worstCase {
		errs = append(errs, fmt.Sprintf("NATS_EVENTS_MAX_AGE=%s shorter than AckWait*MaxDeliver=%s; messages may be discarded mid-retry", n.EventsMaxAge, worstCase))
	}

	if len(errs) == 0 {
		return nil
	}
	return errors.New(strings.Join(errs, "; "))
}

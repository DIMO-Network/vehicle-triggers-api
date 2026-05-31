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
	TokenExchangeGRPCAddr             string         `env:"TOKEN_EXCHANGE_GRPC_ADDR"`
	TokenExchangeCacheExpiration      time.Duration  `env:"TOKEN_EXCHANGE_CACHE_EXPIRATION"`
	TokenExchangeCacheCleanupInterval time.Duration  `env:"TOKEN_EXCHANGE_CACHE_CLEANUP_INTERVAL"`
	VehicleNFTAddress                 common.Address `env:"VEHICLE_NFT_ADDRESS"`
	DIMORegistryChainID               uint64         `env:"DIMO_REGISTRY_CHAIN_ID"`
	MaxWebhookFailureCount            uint           `env:"MAX_WEBHOOK_FAILURE_COUNT"`
	// MaxInFlight is the maximum number of messages to process concurrently per consumer
	MaxInFlight int `env:"MAX_IN_FLIGHT" envDefault:"50"`

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

	NATS       NATSSettings       `envPrefix:"NATS_"`
	// Env prefix kept as NATS_DISPATCHER_*, NATS_AUDIT_*, CACHE_* so the
	// regroup is a pure code-organisation change and prod env files don't
	// have to move. The semantics live with the subsystem; the namespace
	// reflects history.
	Dispatcher DispatcherSettings `envPrefix:"NATS_DISPATCHER_"`
	Audit      AuditSettings      `envPrefix:"NATS_AUDIT_"`
	Cache      CacheSettings      `envPrefix:"CACHE_"`

	DB db.Settings `envPrefix:"DB_"`
}

// DispatcherSettings tunes the webhook delivery worker pool. Owned by
// webhookdispatcher.Dispatcher; lived under NATSSettings historically
// because the env prefix was the path of least resistance.
type DispatcherSettings struct {
	// Workers=0 keeps the legacy synchronous behavior; >0 spins up a worker
	// pool that owns delivery + state + audit + failure-count bookkeeping
	// so a slow receiver can't throttle the consumer.
	Workers           int           `env:"WORKERS" envDefault:"32"`
	QueueSize         int           `env:"QUEUE_SIZE" envDefault:"4096"`
	RetryAttempts     int           `env:"RETRY_ATTEMPTS" envDefault:"2"`
	RetryInitialDelay time.Duration `env:"RETRY_INITIAL_DELAY" envDefault:"100ms"`
	PerHostRPS        float64       `env:"PER_HOST_RPS" envDefault:"0"`
	PerHostBurst      int           `env:"PER_HOST_BURST" envDefault:"0"`
}

// AuditSettings tunes the fire-and-forget audit queue fronting JetStream
// PublishAsync. Sized to absorb a stream-side stall without spawning one
// goroutine per fire.
type AuditSettings struct {
	Workers   int `env:"WORKERS" envDefault:"4"`
	QueueSize int `env:"QUEUE_SIZE" envDefault:"16384"`
}

// CacheSettings tunes the webhookcache rebuild loop.
type CacheSettings struct {
	// DebounceTime is the wait between successive cache refreshes so a
	// CRUD burst collapses into one rebuild.
	DebounceTime time.Duration `env:"DEBOUNCE_TIME"`
	// BuildWorkers caps the parallelism of the per-trigger fetch+CEL
	// compile loop. Each worker takes one DB roundtrip plus one CEL
	// compile at a time, so it doubles as a DB connection-pool guard.
	// Defaults to 2 because the prod pod is pinned to ~1 CPU; raise it
	// on multi-core nodes.
	BuildWorkers int `env:"BUILD_WORKERS" envDefault:"2"`
}

// NATSSettings holds NATS JetStream wiring.
//
// Mode is now effectively a two-valued switch since Kafka was ripped out:
//   - off       (default): service runs the HTTP API only, no ingest.
//   - primary | exclusive: NATS owns the evaluation path. (The two values
//     are kept distinct for backwards-compat with prod env files but
//     behave identically post-Kafka deletion.)
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

	// Dispatcher and Audit live as their own subsystem-typed Settings
	// off the root struct; see Settings.Dispatcher and Settings.Audit.
	// They keep NATS_DISPATCHER_* and NATS_AUDIT_* env prefixes for
	// backwards compatibility with prod env files.

	TriggerStateBucket  string        `env:"TRIGGER_STATE_BUCKET" envDefault:"trigger_state"`
	TriggerStateTTL     time.Duration `env:"TRIGGER_STATE_TTL" envDefault:"168h"` // 7d
	SignalHistoryBucket string        `env:"SIGNAL_HISTORY_BUCKET" envDefault:"signal_history"`
	SignalHistoryTTL    time.Duration `env:"SIGNAL_HISTORY_TTL" envDefault:"168h"` // 7d

	// RateLimitBucket holds the cluster-shared per-host token bucket
	// state for outbound webhook delivery. Empty disables cluster
	// sharing (dispatcher falls back to the per-pod hostLimiter).
	RateLimitBucket string        `env:"RATE_LIMIT_BUCKET" envDefault:"webhook_rate_limit"`
	RateLimitTTL    time.Duration `env:"RATE_LIMIT_TTL" envDefault:"1h"`
}

// Enabled reports whether any NATS wiring should run.
func (n NATSSettings) Enabled() bool { return n.Mode != "" && n.Mode != "off" }

// PrimaryMode reports whether NATS owns the evaluation path. Equivalent to
// Enabled() post-Kafka-deletion; kept as a separate method so the wiring
// reads naturally ("if NATS is the primary ingest path") and a future split
// (e.g. "evaluator-only, no consumer" Mode) has a place to land.
func (n NATSSettings) PrimaryMode() bool { return n.Enabled() }

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

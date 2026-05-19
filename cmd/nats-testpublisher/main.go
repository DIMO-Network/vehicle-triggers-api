// nats-testpublisher is a developer tool that stands in for the DIS service.
// It synthesizes vss SignalCloudEvent / EventCloudEvent payloads and publishes
// them to the JetStream subjects the vehicle-triggers-api service consumes.
//
// Example:
//
//	nats-testpublisher -url nats://localhost:4222 \
//	    -rate 50 -vehicles 100 -signals speed,fuelLevel,odometer \
//	    -mode both
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"math/rand/v2"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/model-garage/pkg/vss"
	vtnats "github.com/DIMO-Network/vehicle-triggers-api/internal/nats"
	"github.com/ethereum/go-ethereum/common"
	nc "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type mode string

const (
	modeSignals mode = "signals"
	modeEvents  mode = "events"
	modeBoth    mode = "both"
)

type config struct {
	URL       string
	CredsFile string

	Rate     float64
	Vehicles int
	ChainID  uint64
	Contract string

	Signals []string
	Events  []string
	Mode    mode

	Duration time.Duration
	Burst    int
	Jitter   time.Duration

	EnsureStreams  bool
	SignalsStream  string
	EventsStream   string
	SignalsSubject string
	EventsSubject  string
	MaxAge         time.Duration

	ReplayFrom string
}

func parseConfig() (*config, error) {
	cfg := &config{}
	var sigList, evtList string
	var m string

	flag.StringVar(&cfg.URL, "url", "nats://localhost:4222", "NATS URL")
	flag.StringVar(&cfg.CredsFile, "creds", "", "NATS credentials file")
	flag.Float64Var(&cfg.Rate, "rate", 10, "Messages per second (per mode). 0 = as fast as possible")
	flag.IntVar(&cfg.Vehicles, "vehicles", 10, "Number of distinct synthetic vehicle token IDs")
	flag.Uint64Var(&cfg.ChainID, "chain-id", 137, "EVM chain ID to stamp in the ERC721 DID")
	flag.StringVar(&cfg.Contract, "contract", "0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF", "Vehicle NFT contract address")
	flag.StringVar(&sigList, "signals", "speed,fuelLevel,odometer,batteryVoltage", "Comma-separated signal names")
	flag.StringVar(&evtList, "events", "harshBraking,ignitionOn,ignitionOff", "Comma-separated event names")
	flag.StringVar(&m, "mode", "both", "signals|events|both")
	flag.DurationVar(&cfg.Duration, "duration", 0, "Run for this long then exit. 0 = run forever until Ctrl-C")
	flag.IntVar(&cfg.Burst, "burst", 1, "Messages emitted per tick")
	flag.DurationVar(&cfg.Jitter, "jitter", 0, "Random extra delay 0..jitter between ticks")
	flag.BoolVar(&cfg.EnsureStreams, "ensure-streams", true, "Create/update DIMO_SIGNALS and DIMO_EVENTS streams before publishing")
	flag.StringVar(&cfg.SignalsStream, "signals-stream", "DIMO_SIGNALS", "Signals stream name")
	flag.StringVar(&cfg.EventsStream, "events-stream", "DIMO_EVENTS", "Events stream name")
	flag.StringVar(&cfg.SignalsSubject, "signals-subject", "dimo.signals.>", "Signals stream subject filter")
	flag.StringVar(&cfg.EventsSubject, "events-subject", "dimo.events.>", "Events stream subject filter")
	flag.DurationVar(&cfg.MaxAge, "max-age", 24*time.Hour, "Stream MaxAge when ensuring streams")
	flag.StringVar(&cfg.ReplayFrom, "replay-from", "", "Path to a jsonl file of captured payloads to replay")
	flag.Parse()

	cfg.Signals = splitCSV(sigList)
	cfg.Events = splitCSV(evtList)
	cfg.Mode = mode(strings.ToLower(m))
	switch cfg.Mode {
	case modeSignals, modeEvents, modeBoth:
	default:
		return nil, fmt.Errorf("invalid -mode %q (signals|events|both)", m)
	}
	if cfg.Vehicles < 1 {
		return nil, errors.New("-vehicles must be >= 1")
	}
	if cfg.Mode != modeEvents && len(cfg.Signals) == 0 {
		return nil, errors.New("at least one -signals name required")
	}
	if cfg.Mode != modeSignals && len(cfg.Events) == 0 {
		return nil, errors.New("at least one -events name required")
	}
	if !common.IsHexAddress(cfg.Contract) {
		return nil, fmt.Errorf("-contract %q is not a hex address", cfg.Contract)
	}
	return cfg, nil
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func main() {
	cfg, err := parseConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		flag.Usage()
		os.Exit(2)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if cfg.Duration > 0 {
		ctx2, cancel2 := context.WithTimeout(ctx, cfg.Duration)
		defer cancel2()
		ctx = ctx2
	}

	opts := []nc.Option{nc.Name("nats-testpublisher"), nc.Timeout(5 * time.Second)}
	if cfg.CredsFile != "" {
		opts = append(opts, nc.UserCredentials(cfg.CredsFile))
	}
	conn, err := nc.Connect(cfg.URL, opts...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nats connect:", err)
		os.Exit(1)
	}
	defer conn.Drain() //nolint:errcheck

	js, err := jetstream.New(conn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jetstream:", err)
		os.Exit(1)
	}

	if cfg.EnsureStreams {
		if err := ensureStreams(ctx, js, cfg); err != nil {
			fmt.Fprintln(os.Stderr, "ensure streams:", err)
			os.Exit(1)
		}
	}

	if cfg.ReplayFrom != "" {
		if err := replayFile(ctx, js, cfg); err != nil {
			fmt.Fprintln(os.Stderr, "replay:", err)
			os.Exit(1)
		}
		return
	}

	if err := loop(ctx, js, cfg); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		fmt.Fprintln(os.Stderr, "loop:", err)
		os.Exit(1)
	}
}

func ensureStreams(ctx context.Context, js jetstream.JetStream, cfg *config) error {
	specs := []jetstream.StreamConfig{
		{Name: cfg.SignalsStream, Subjects: []string{cfg.SignalsSubject}, Retention: jetstream.LimitsPolicy, Discard: jetstream.DiscardOld, Storage: jetstream.FileStorage, MaxAge: cfg.MaxAge, Replicas: 1},
		{Name: cfg.EventsStream, Subjects: []string{cfg.EventsSubject}, Retention: jetstream.LimitsPolicy, Discard: jetstream.DiscardOld, Storage: jetstream.FileStorage, MaxAge: cfg.MaxAge, Replicas: 1},
	}
	for _, s := range specs {
		if _, err := js.CreateOrUpdateStream(ctx, s); err != nil {
			return fmt.Errorf("stream %s: %w", s.Name, err)
		}
	}
	return nil
}

func loop(ctx context.Context, js jetstream.JetStream, cfg *config) error {
	var interval time.Duration
	if cfg.Rate > 0 {
		interval = time.Duration(float64(time.Second) / cfg.Rate)
	}
	contract := common.HexToAddress(cfg.Contract)

	var sent atomic.Uint64
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		last := uint64(0)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				cur := sent.Load()
				fmt.Printf("[testpublisher] sent=%d rate=%.1f/s\n", cur, float64(cur-last)/5)
				last = cur
			}
		}
	}()

	publish := func() error {
		tokenID := rand.IntN(cfg.Vehicles) + 1
		did := cloudevent.ERC721DID{ChainID: cfg.ChainID, ContractAddress: contract, TokenID: big.NewInt(int64(tokenID))}
		switch cfg.Mode {
		case modeSignals:
			return publishSignalBatch(ctx, js, cfg, did)
		case modeEvents:
			return publishEventBatch(ctx, js, cfg, did)
		case modeBoth:
			if rand.IntN(2) == 0 {
				return publishSignalBatch(ctx, js, cfg, did)
			}
			return publishEventBatch(ctx, js, cfg, did)
		}
		return nil
	}

	tick := func() error {
		for i := 0; i < cfg.Burst; i++ {
			if err := publish(); err != nil {
				return err
			}
			sent.Add(1)
		}
		return nil
	}

	if interval == 0 {
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				if err := tick(); err != nil {
					return err
				}
			}
		}
	}

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := tick(); err != nil {
				return err
			}
			if cfg.Jitter > 0 {
				time.Sleep(time.Duration(rand.Int64N(int64(cfg.Jitter))))
			}
		}
	}
}

func publishSignalBatch(ctx context.Context, js jetstream.JetStream, cfg *config, did cloudevent.ERC721DID) error {
	name := cfg.Signals[rand.IntN(len(cfg.Signals))]
	now := time.Now().UTC()
	hdr := cloudevent.CloudEventHeader{
		SpecVersion:     "1.0",
		Type:            "dimo.signal",
		Source:          "nats-testpublisher",
		Subject:         did.String(),
		ID:              fmt.Sprintf("tp-sig-%d-%d", now.UnixNano(), rand.Uint32()),
		Time:            now,
		DataContentType: "application/json",
		Producer:        "nats-testpublisher/synthetic",
	}
	sig := vss.Signal{
		CloudEventHeader: hdr,
		Data: vss.SignalData{
			Timestamp:   now,
			Name:        name,
			ValueNumber: rand.Float64() * 120,
		},
	}
	ce := vss.PackSignals(hdr, []vss.Signal{sig})
	payload, err := json.Marshal(ce)
	if err != nil {
		return err
	}
	subject := vtnats.SignalSubject(name)
	_, err = js.Publish(ctx, subject, payload)
	return err
}

func publishEventBatch(ctx context.Context, js jetstream.JetStream, cfg *config, did cloudevent.ERC721DID) error {
	name := cfg.Events[rand.IntN(len(cfg.Events))]
	now := time.Now().UTC()
	hdr := cloudevent.CloudEventHeader{
		SpecVersion:     "1.0",
		Type:            "dimo.event",
		Source:          "nats-testpublisher",
		Subject:         did.String(),
		ID:              fmt.Sprintf("tp-evt-%d-%d", now.UnixNano(), rand.Uint32()),
		Time:            now,
		DataContentType: "application/json",
		Producer:        "nats-testpublisher/synthetic",
	}
	evt := vss.Event{
		CloudEventHeader: hdr,
		Data: vss.EventData{
			Name:       name,
			Timestamp:  now,
			DurationNs: uint64(rand.IntN(5_000_000_000)),
		},
	}
	ce := vss.PackEvents(hdr, []vss.Event{evt})
	payload, err := json.Marshal(ce)
	if err != nil {
		return err
	}
	subject := vtnats.EventSubject(name)
	_, err = js.Publish(ctx, subject, payload)
	return err
}

// replayFile reads a jsonl file of captured payloads and re-publishes them.
// Each line: {"subject":"dimo.signals.123.speed","payload":<base64 or json object>}
// If "payload" is a JSON object, it is re-marshaled; otherwise it must be a string
// (interpreted as the raw body to publish).
func replayFile(ctx context.Context, js jetstream.JetStream, cfg *config) error {
	f, err := os.Open(cfg.ReplayFrom)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // read-only file, close error is uninteresting

	dec := json.NewDecoder(f)
	var count int
	for dec.More() {
		var rec struct {
			Subject string          `json:"subject"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := dec.Decode(&rec); err != nil {
			return fmt.Errorf("decode line %d: %w", count, err)
		}
		if rec.Subject == "" || len(rec.Payload) == 0 {
			continue
		}
		if _, err := js.Publish(ctx, rec.Subject, rec.Payload); err != nil {
			return fmt.Errorf("publish %s: %w", rec.Subject, err)
		}
		count++
	}
	fmt.Printf("[testpublisher] replayed %d messages\n", count)
	return nil
}

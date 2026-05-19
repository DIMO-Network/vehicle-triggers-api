// triggers-bench is a load + latency probe for the vehicle-triggers-api NATS
// JetStream ingest path. It publishes synthetic vss SignalCloudEvent payloads
// at a target rate, pulls them back through a JetStream consumer that mirrors
// the production filter subjects, and reports throughput plus publish/round-
// trip latency percentiles.
//
// Example:
//
//	triggers-bench -url nats://localhost:4222 \
//	    -rate 5000 -duration 30s -signals speed,fuelLevel,odometer \
//	    -consumers 4
//
// Use a transient stream name so concurrent runs don't trample each other.
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"math/rand/v2"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
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

type config struct {
	URL       string
	CredsFile string

	Rate     float64
	Duration time.Duration
	Vehicles int
	Signals  []string
	Workers    int
	Publishers int
	Replicas   int
	Async      bool

	StreamName string
	Subject    string
	ConsumerN  string

	NoConsume bool
}

func parseConfig() (*config, error) {
	cfg := &config{}
	var sigList string
	flag.StringVar(&cfg.URL, "url", "nats://localhost:4222", "NATS URL")
	flag.StringVar(&cfg.CredsFile, "creds", "", "NATS credentials file (optional)")
	flag.Float64Var(&cfg.Rate, "rate", 1000, "Target publishes per second")
	flag.DurationVar(&cfg.Duration, "duration", 30*time.Second, "How long to publish")
	flag.IntVar(&cfg.Vehicles, "vehicles", 10000, "Number of synthetic vehicle token IDs")
	flag.StringVar(&sigList, "signals", "speed,fuelLevel,odometer,batteryVoltage", "Comma-separated signal names")
	flag.IntVar(&cfg.Workers, "consumers", 1, "Concurrent NATS pull loops")
	flag.IntVar(&cfg.Publishers, "publishers", 1, "Concurrent publisher goroutines (each gets rate/N)")
	flag.IntVar(&cfg.Replicas, "replicas", 1, "Stream replication factor (1, 3, 5...)")
	flag.BoolVar(&cfg.Async, "async", false, "Use PublishAsync instead of sync Publish for higher throughput")
	flag.StringVar(&cfg.StreamName, "stream", "BENCH_SIGNALS", "JetStream stream name (created/updated)")
	flag.StringVar(&cfg.Subject, "subject", "bench.signals.>", "Stream subject")
	flag.StringVar(&cfg.ConsumerN, "consumer-name", "bench-cons", "Durable consumer name (random suffix appended)")
	flag.BoolVar(&cfg.NoConsume, "no-consume", false, "Skip the consumer side (publish-only benchmark)")
	flag.Parse()
	cfg.Signals = splitCSV(sigList)
	if len(cfg.Signals) == 0 {
		return nil, errors.New("at least one -signals name required")
	}
	if cfg.Rate <= 0 {
		return nil, errors.New("-rate must be > 0")
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
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
}

func run(cfg *config) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	opts := []nc.Option{nc.Name("triggers-bench"), nc.Timeout(5 * time.Second)}
	if cfg.CredsFile != "" {
		opts = append(opts, nc.UserCredentials(cfg.CredsFile))
	}
	conn, err := nc.Connect(cfg.URL, opts...)
	if err != nil {
		return fmt.Errorf("nats connect: %w", err)
	}
	defer conn.Drain() //nolint:errcheck // benchmark teardown, best-effort

	js, err := jetstream.New(conn, jetstream.WithPublishAsyncMaxPending(50_000))
	if err != nil {
		return fmt.Errorf("jetstream: %w", err)
	}

	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      cfg.StreamName,
		Subjects:  []string{cfg.Subject},
		Retention: jetstream.LimitsPolicy,
		Discard:   jetstream.DiscardOld,
		Storage:   jetstream.FileStorage,
		MaxAge:    10 * time.Minute,
		Replicas:  cfg.Replicas,
	}); err != nil {
		return fmt.Errorf("ensure stream: %w", err)
	}
	defer js.DeleteStream(context.Background(), cfg.StreamName) //nolint:errcheck // benchmark teardown

	// Stats collectors. publishSamples records ack latency; rtSamples records
	// publish-to-consume latency. Both cap at duration*rate, then sample.
	pubSamples := newSamples(int(cfg.Rate*cfg.Duration.Seconds()) + 1024)
	rtSamples := newSamples(int(cfg.Rate*cfg.Duration.Seconds()) + 1024)

	var publishedOK, publishedErr, consumed uint64

	var wg sync.WaitGroup
	consumerDone := make(chan struct{})
	if !cfg.NoConsume {
		consumerName := fmt.Sprintf("%s-%d", cfg.ConsumerN, time.Now().UnixNano())
		cons, err := js.CreateOrUpdateConsumer(ctx, cfg.StreamName, jetstream.ConsumerConfig{
			Durable:       consumerName,
			AckPolicy:     jetstream.AckExplicitPolicy,
			DeliverPolicy: jetstream.DeliverAllPolicy,
			FilterSubject: cfg.Subject,
			MaxAckPending: 50_000,
		})
		if err != nil {
			return fmt.Errorf("ensure consumer: %w", err)
		}
		defer js.DeleteConsumer(context.Background(), cfg.StreamName, consumerName) //nolint:errcheck // benchmark teardown

		for i := 0; i < cfg.Workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				runConsumer(ctx, cons, rtSamples, &consumed)
			}()
		}
		go func() { wg.Wait(); close(consumerDone) }()
	} else {
		close(consumerDone)
	}

	// Periodic progress line so the operator can see liveness during long runs.
	progressDone := make(chan struct{})
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		var lastPub uint64
		var lastCons uint64
		for {
			select {
			case <-ctx.Done():
				close(progressDone)
				return
			case <-t.C:
				p := atomic.LoadUint64(&publishedOK)
				c := atomic.LoadUint64(&consumed)
				fmt.Printf("[bench] pub=%d (+%d) cons=%d (+%d) errs=%d\n",
					p, p-lastPub, c, c-lastCons, atomic.LoadUint64(&publishedErr))
				lastPub = p
				lastCons = c
			}
		}
	}()

	pubCtx, pubCancel := context.WithTimeout(ctx, cfg.Duration)
	defer pubCancel()

	start := time.Now()
	var pubWG sync.WaitGroup
	pubCount := cfg.Publishers
	if pubCount < 1 {
		pubCount = 1
	}
	perPubRate := cfg.Rate / float64(pubCount)
	for i := 0; i < pubCount; i++ {
		pubWG.Add(1)
		go func() {
			defer pubWG.Done()
			runPublisher(pubCtx, js, cfg, perPubRate, pubSamples, &publishedOK, &publishedErr)
		}()
	}
	pubWG.Wait()
	if cfg.Async {
		// Wait for all async acks to land so we measure to-server-ack, not
		// just to-network. Bounded by a hard ceiling so a stuck server
		// doesn't hang the bench.
		select {
		case <-js.PublishAsyncComplete():
		case <-time.After(30 * time.Second):
			fmt.Fprintln(os.Stderr, "[bench] async publish drain timed out")
		}
	}
	pubElapsed := time.Since(start)

	// Drain consumer: wait briefly for trailing messages to land, then stop.
	if !cfg.NoConsume {
		drainDeadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(drainDeadline) {
			before := atomic.LoadUint64(&consumed)
			time.Sleep(500 * time.Millisecond)
			after := atomic.LoadUint64(&consumed)
			if after == before && after >= atomic.LoadUint64(&publishedOK) {
				break
			}
		}
		cancel()
		select {
		case <-consumerDone:
		case <-time.After(5 * time.Second):
			fmt.Fprintln(os.Stderr, "[bench] consumers did not exit cleanly")
		}
	}
	<-progressDone

	report(cfg, pubElapsed, &publishedOK, &publishedErr, &consumed, pubSamples, rtSamples)
	return nil
}

func runPublisher(ctx context.Context, js jetstream.JetStream, cfg *config, rate float64, samples *samples, ok, errs *uint64) {
	contract := common.HexToAddress("0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF")
	interval := time.Duration(float64(time.Second) / rate)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tokenID := rand.IntN(cfg.Vehicles) + 1
			signal := cfg.Signals[rand.IntN(len(cfg.Signals))]
			payload := buildPayload(cfg, contract, tokenID, signal)
			subject := strings.Replace(cfg.Subject, ".>", "."+signal, 1)
			t0 := time.Now()
			if cfg.Async {
				future, err := js.PublishAsync(subject, payload)
				if err != nil {
					atomic.AddUint64(errs, 1)
					continue
				}
				atomic.AddUint64(ok, 1)
				go func() {
					select {
					case <-future.Ok():
						samples.add(time.Since(t0))
					case <-future.Err():
						atomic.AddUint64(errs, 1)
					}
				}()
				continue
			}
			_, err := js.Publish(ctx, subject, payload)
			lat := time.Since(t0)
			if err != nil {
				atomic.AddUint64(errs, 1)
				continue
			}
			atomic.AddUint64(ok, 1)
			samples.add(lat)
		}
	}
}

// buildPayload synthesizes a SignalCloudEvent with the publish timestamp
// embedded in the Producer field so the consumer can compute roundtrip time
// without needing message headers.
func buildPayload(_ *config, contract common.Address, tokenID int, name string) []byte {
	now := time.Now().UTC()
	did := cloudevent.ERC721DID{ChainID: 137, ContractAddress: contract, TokenID: big.NewInt(int64(tokenID))}
	hdr := cloudevent.CloudEventHeader{
		SpecVersion:     "1.0",
		Type:            "dimo.signal",
		Source:          "triggers-bench",
		Subject:         did.String(),
		ID:              fmt.Sprintf("bench-%d", now.UnixNano()),
		Time:            now,
		DataContentType: "application/json",
		Producer:        encodeProducer(now),
	}
	sig := vss.Signal{CloudEventHeader: hdr, Data: vss.SignalData{
		Timestamp: now, Name: name, ValueNumber: rand.Float64() * 120,
	}}
	ce := vss.PackSignals(hdr, []vss.Signal{sig})
	b, _ := json.Marshal(ce)
	return b
}

// encodeProducer stashes the publish wall time inside the Producer string so
// the consumer can recover it without a separate header parse. We use raw
// little-endian int64 (UnixNano) base16'd to avoid JSON escaping issues.
func encodeProducer(t time.Time) string {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(t.UnixNano()))
	return "bench/" + hex(buf[:])
}

func decodeProducer(s string) (time.Time, bool) {
	const prefix = "bench/"
	if !strings.HasPrefix(s, prefix) {
		return time.Time{}, false
	}
	raw, ok := unhex(s[len(prefix):])
	if !ok || len(raw) != 8 {
		return time.Time{}, false
	}
	return time.Unix(0, int64(binary.LittleEndian.Uint64(raw))), true
}

const hexDigits = "0123456789abcdef"

func hex(b []byte) string {
	out := make([]byte, len(b)*2)
	for i, x := range b {
		out[i*2] = hexDigits[x>>4]
		out[i*2+1] = hexDigits[x&0x0f]
	}
	return string(out)
}

func unhex(s string) ([]byte, bool) {
	if len(s)%2 != 0 {
		return nil, false
	}
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		hi := hexVal(s[2*i])
		lo := hexVal(s[2*i+1])
		if hi < 0 || lo < 0 {
			return nil, false
		}
		out[i] = byte(hi<<4 | lo)
	}
	return out, true
}

func hexVal(b byte) int {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0')
	case b >= 'a' && b <= 'f':
		return int(b-'a') + 10
	case b >= 'A' && b <= 'F':
		return int(b-'A') + 10
	}
	return -1
}

func runConsumer(ctx context.Context, cons jetstream.Consumer, samples *samples, consumed *uint64) {
	iter, err := cons.Messages(jetstream.PullMaxMessages(500))
	if err != nil {
		return
	}
	defer iter.Stop()
	go func() {
		<-ctx.Done()
		iter.Stop()
	}()
	for {
		msg, err := iter.Next()
		if err != nil {
			return
		}
		var ce vss.SignalCloudEvent
		if json.Unmarshal(msg.Data(), &ce) == nil {
			if pubT, ok := decodeProducer(ce.Producer); ok {
				samples.add(time.Since(pubT))
			}
		}
		_ = msg.Ack()
		atomic.AddUint64(consumed, 1)
	}
}

func report(cfg *config, pubElapsed time.Duration, ok, errs, consumed *uint64, pubS *samples, rtS *samples) {
	pubCount := atomic.LoadUint64(ok)
	errCount := atomic.LoadUint64(errs)
	consCount := atomic.LoadUint64(consumed)
	throughput := float64(pubCount) / pubElapsed.Seconds()
	fmt.Println()
	fmt.Println("=== triggers-bench results ===")
	fmt.Printf("target rate           : %.0f msg/s for %s (publishers=%d, replicas=%d, async=%v)\n", cfg.Rate, cfg.Duration, cfg.Publishers, cfg.Replicas, cfg.Async)
	fmt.Printf("publisher elapsed     : %s\n", pubElapsed)
	fmt.Printf("published ok / err    : %d / %d\n", pubCount, errCount)
	fmt.Printf("publish throughput    : %.1f msg/s\n", throughput)
	if !cfg.NoConsume {
		fmt.Printf("consumed              : %d (delta=%d)\n", consCount, int64(pubCount)-int64(consCount))
	}
	fmt.Println()
	fmt.Println("publish-ack latency:")
	printLatency(pubS)
	if !cfg.NoConsume {
		fmt.Println("\npublish->consume roundtrip latency:")
		printLatency(rtS)
	}
}

func printLatency(s *samples) {
	xs := s.snapshot()
	if len(xs) == 0 {
		fmt.Println("  (no samples)")
		return
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	pct := func(p float64) time.Duration {
		idx := int(float64(len(xs)-1) * p)
		return xs[idx]
	}
	fmt.Printf("  n=%d  min=%s  p50=%s  p90=%s  p95=%s  p99=%s  max=%s\n",
		len(xs), xs[0], pct(0.50), pct(0.90), pct(0.95), pct(0.99), xs[len(xs)-1])
}

// samples is a lock-protected slice of latency observations. Caller passes a
// hint for max capacity to avoid runaway memory; once full it down-samples by
// keeping every other entry on overflow.
type samples struct {
	mu      sync.Mutex
	data    []time.Duration
	maxCap  int
	stride  int
	counter int
}

func newSamples(cap int) *samples {
	if cap < 1024 {
		cap = 1024
	}
	return &samples{data: make([]time.Duration, 0, cap), maxCap: cap, stride: 1}
}

func (s *samples) add(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counter++
	if s.counter%s.stride != 0 {
		return
	}
	if len(s.data) >= s.maxCap {
		// Halve resolution: drop every other sample, double stride.
		half := s.data[:0]
		for i := 0; i < len(s.data); i += 2 {
			half = append(half, s.data[i])
		}
		s.data = half
		s.stride *= 2
	}
	s.data = append(s.data, d)
}

func (s *samples) snapshot() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]time.Duration, len(s.data))
	copy(out, s.data)
	return out
}

// ensure vtnats is used (subjects derived from production helpers when
// callers want exact production parity in -subject flags they construct
// from outside this tool).
var _ = vtnats.SignalSubject

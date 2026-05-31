// triggers-kvbench measures sustained Put rate against a JetStream KV bucket.
//
// Background: PROD_HARDENING_V2.md item G calls out that at 30k fires/sec
// the dispatcher writes 60k KV puts/sec (trigger_state + signal_history).
// The cluster bench in cmd/triggers-bench measures stream publish + pull
// throughput; KV writes are a different code path with their own ceiling
// (each Put is a JetStream message + key index update). This binary
// isolates that path so the cutover plan has a measured number, not a
// model.
//
// Usage:
//
//	go run ./cmd/triggers-kvbench \
//	    -url=nats://localhost:4222 \
//	    -bucket=bench_kv \
//	    -keys=10000 \
//	    -concurrency=64 \
//	    -duration=30s
//
// Output: rolling Put/sec, p50/p95/p99 latency, error count. The bucket is
// created with TTL=10m and replicas=1 by default - adjust per cluster
// shape. Cleans up on exit.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type config struct {
	url         string
	bucket      string
	keys        int
	concurrency int
	duration    time.Duration
	ttl         time.Duration
	replicas    int
	valueSize   int
	keep        bool
}

func main() {
	cfg := config{}
	flag.StringVar(&cfg.url, "url", "nats://localhost:4222", "NATS URL")
	flag.StringVar(&cfg.bucket, "bucket", "bench_kv", "KV bucket name")
	flag.IntVar(&cfg.keys, "keys", 10000, "key cardinality (cycle through this many keys)")
	flag.IntVar(&cfg.concurrency, "concurrency", 64, "parallel writer goroutines")
	flag.DurationVar(&cfg.duration, "duration", 30*time.Second, "test duration")
	flag.DurationVar(&cfg.ttl, "ttl", 10*time.Minute, "bucket TTL")
	flag.IntVar(&cfg.replicas, "replicas", 1, "bucket replicas")
	flag.IntVar(&cfg.valueSize, "value-size", 256, "value size in bytes")
	flag.BoolVar(&cfg.keep, "keep", false, "leave bucket behind on exit")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	conn, err := nats.Connect(cfg.url)
	if err != nil {
		log.Fatalf("connect: %s", err)
	}
	defer conn.Close()
	js, err := jetstream.New(conn)
	if err != nil {
		log.Fatalf("jetstream: %s", err)
	}

	kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:   cfg.bucket,
		TTL:      cfg.ttl,
		Replicas: cfg.replicas,
	})
	if err != nil {
		log.Fatalf("create kv: %s", err)
	}
	if !cfg.keep {
		defer func() {
			delCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = js.DeleteKeyValue(delCtx, cfg.bucket)
		}()
	}

	value := make([]byte, cfg.valueSize)
	for i := range value {
		value[i] = byte(i % 256)
	}

	deadline, cancelDeadline := context.WithTimeout(ctx, cfg.duration)
	defer cancelDeadline()

	var (
		writes    atomic.Uint64
		errs      atomic.Uint64
		latNanos  = make(chan int64, cfg.concurrency*256)
		latencies []int64
		latMu     sync.Mutex
	)

	go func() {
		for ns := range latNanos {
			latMu.Lock()
			latencies = append(latencies, ns)
			latMu.Unlock()
		}
	}()

	start := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < cfg.concurrency; w++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			r := rand.New(rand.NewSource(seed)) //nolint:gosec // bench-only PRNG
			for deadline.Err() == nil {
				key := fmt.Sprintf("k%d", r.Intn(cfg.keys))
				putStart := time.Now()
				_, err := kv.Put(deadline, key, value)
				lat := time.Since(putStart).Nanoseconds()
				if err != nil {
					if deadline.Err() == nil {
						errs.Add(1)
					}
					continue
				}
				writes.Add(1)
				select {
				case latNanos <- lat:
				default:
					// shedding latency samples is fine - we keep a rolling
					// average via the count; the histogram is best-effort.
				}
			}
		}(int64(w))
	}

	// Per-second progress so the run is observable.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	last := uint64(0)
	for deadline.Err() == nil {
		select {
		case <-deadline.Done():
		case <-ticker.C:
			cur := writes.Load()
			fmt.Printf("[%s] writes=%d (+%d /s) errs=%d\n",
				time.Since(start).Round(time.Second), cur, cur-last, errs.Load())
			last = cur
		}
	}
	wg.Wait()
	close(latNanos)

	elapsed := time.Since(start)
	total := writes.Load()
	failed := errs.Load()

	fmt.Println()
	fmt.Println("== KV write bench ==")
	fmt.Printf("bucket=%s keys=%d concurrency=%d ttl=%s replicas=%d value=%dB\n",
		cfg.bucket, cfg.keys, cfg.concurrency, cfg.ttl, cfg.replicas, cfg.valueSize)
	fmt.Printf("duration=%s writes=%d errors=%d throughput=%.0f /s\n",
		elapsed.Round(10*time.Millisecond), total, failed, float64(total)/elapsed.Seconds())

	latMu.Lock()
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	if n := len(latencies); n > 0 {
		fmt.Printf("latency p50=%s p95=%s p99=%s max=%s (%d samples)\n",
			time.Duration(latencies[n/2]),
			time.Duration(latencies[n*95/100]),
			time.Duration(latencies[n*99/100]),
			time.Duration(latencies[n-1]),
			n,
		)
	}
	latMu.Unlock()
}

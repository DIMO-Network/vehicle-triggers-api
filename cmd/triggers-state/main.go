// triggers-state is an operator tool for reading the trigger_state KV
// bucket. It is the canonical way to inspect distributed cooldown state -
// "did this trigger fire recently for this vehicle?" - across the running
// vehicle-triggers-api replicas without touching Postgres.
//
// Subcommands:
//
//	triggers-state list                       # print every key in the bucket
//	triggers-state get  <triggerID> <DID>     # print one record (JSON)
//	triggers-state dump                       # print every key+value (JSONL)
//	triggers-state watch                      # tail bucket updates until Ctrl-C
//
// All commands accept -url and -bucket. The DID argument for `get` is the
// full ERC721 DID string (did:erc721:<chain>:<contract>:<tokenId>).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/DIMO-Network/cloudevent"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggerstate"
	"github.com/ethereum/go-ethereum/common"
	nc "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type flags struct {
	URL       string
	CredsFile string
	Bucket    string
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	sub := os.Args[1]
	rest := os.Args[2:]

	fs := flag.NewFlagSet(sub, flag.ExitOnError)
	f := flags{}
	fs.StringVar(&f.URL, "url", "nats://localhost:4222", "NATS URL")
	fs.StringVar(&f.CredsFile, "creds", "", "NATS credentials file (optional)")
	fs.StringVar(&f.Bucket, "bucket", "trigger_state", "KV bucket name")

	switch sub {
	case "list":
		_ = fs.Parse(rest)
		mustRun(cmdList(f))
	case "get":
		_ = fs.Parse(rest)
		args := fs.Args()
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: triggers-state get [flags] <triggerID> <assetDID>")
			os.Exit(2)
		}
		mustRun(cmdGet(f, args[0], args[1]))
	case "dump":
		_ = fs.Parse(rest)
		mustRun(cmdDump(f))
	case "watch":
		_ = fs.Parse(rest)
		mustRun(cmdWatch(f))
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: triggers-state {list|get|dump|watch} [flags]")
}

func mustRun(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func open(f flags) (*nc.Conn, jetstream.KeyValue, error) {
	opts := []nc.Option{nc.Name("triggers-state"), nc.Timeout(5 * time.Second)}
	if f.CredsFile != "" {
		opts = append(opts, nc.UserCredentials(f.CredsFile))
	}
	conn, err := nc.Connect(f.URL, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("nats connect: %w", err)
	}
	js, err := jetstream.New(conn)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("jetstream: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	kv, err := js.KeyValue(ctx, f.Bucket)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("kv bucket %q: %w", f.Bucket, err)
	}
	return conn, kv, nil
}

func cmdList(f flags) error {
	conn, kv, err := open(f)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	keyLister, err := kv.ListKeys(ctx)
	if err != nil {
		return fmt.Errorf("list keys: %w", err)
	}
	defer func() { _ = keyLister.Stop() }()
	for k := range keyLister.Keys() {
		fmt.Println(k)
	}
	return nil
}

func cmdGet(f flags, triggerID, did string) error {
	conn, kv, err := open(f)
	if err != nil {
		return err
	}
	defer conn.Close()

	parsed, err := parseDID(did)
	if err != nil {
		return err
	}
	key := triggerstate.Key(triggerID, parsed)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entry, err := kv.Get(ctx, key)
	if err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			return fmt.Errorf("no record for %s (trigger=%s vehicle=%s)", key, triggerID, did)
		}
		return fmt.Errorf("kv get %q: %w", key, err)
	}
	var rec triggerstate.Record
	if err := json.Unmarshal(entry.Value(), &rec); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	out := map[string]any{
		"key":          key,
		"revision":     entry.Revision(),
		"created":      entry.Created(),
		"lastFiredAt":  rec.LastFiredAt,
		"triggerId":    rec.TriggerID,
		"assetDid":     rec.AssetDID,
		"ageSeconds":   time.Since(rec.LastFiredAt).Seconds(),
		"lastSnapshot": json.RawMessage(rec.LastSnapshot),
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func cmdDump(f flags) error {
	conn, kv, err := open(f)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	keyLister, err := kv.ListKeys(ctx)
	if err != nil {
		return fmt.Errorf("list keys: %w", err)
	}
	defer func() { _ = keyLister.Stop() }()
	enc := json.NewEncoder(os.Stdout)
	for k := range keyLister.Keys() {
		entry, err := kv.Get(ctx, k)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip %q: %v\n", k, err)
			continue
		}
		var rec triggerstate.Record
		if err := json.Unmarshal(entry.Value(), &rec); err != nil {
			fmt.Fprintf(os.Stderr, "skip %q (decode): %v\n", k, err)
			continue
		}
		_ = enc.Encode(map[string]any{
			"key":         k,
			"revision":    entry.Revision(),
			"created":     entry.Created(),
			"lastFiredAt": rec.LastFiredAt,
			"triggerId":   rec.TriggerID,
			"assetDid":    rec.AssetDID,
		})
	}
	return nil
}

func cmdWatch(f flags) error {
	conn, kv, err := open(f)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	watcher, err := kv.WatchAll(ctx)
	if err != nil {
		return fmt.Errorf("watch: %w", err)
	}
	defer func() { _ = watcher.Stop() }()
	enc := json.NewEncoder(os.Stdout)
	for entry := range watcher.Updates() {
		if entry == nil {
			// Initial replay finished. Continue waiting for live updates.
			continue
		}
		var rec triggerstate.Record
		_ = json.Unmarshal(entry.Value(), &rec)
		_ = enc.Encode(map[string]any{
			"op":          entry.Operation().String(),
			"key":         entry.Key(),
			"revision":    entry.Revision(),
			"updated":     entry.Created(),
			"lastFiredAt": rec.LastFiredAt,
		})
	}
	return nil
}

// parseDID accepts the standard ERC721 DID string
// "did:erc721:<chain>:<contract>:<tokenId>" and returns the parsed struct.
// The DID grammar is strict enough that we can do this without pulling in the
// cloudevent package's full parser.
func parseDID(s string) (cloudevent.ERC721DID, error) {
	const prefix = "did:erc721:"
	if !strings.HasPrefix(s, prefix) {
		return cloudevent.ERC721DID{}, fmt.Errorf("invalid DID (missing %q prefix): %q", prefix, s)
	}
	parts := strings.Split(s[len(prefix):], ":")
	if len(parts) != 3 {
		return cloudevent.ERC721DID{}, fmt.Errorf("invalid DID (need chain:contract:tokenId): %q", s)
	}
	chain := new(big.Int)
	if _, ok := chain.SetString(parts[0], 10); !ok {
		return cloudevent.ERC721DID{}, fmt.Errorf("invalid chain id %q", parts[0])
	}
	if !common.IsHexAddress(parts[1]) {
		return cloudevent.ERC721DID{}, fmt.Errorf("invalid contract address %q", parts[1])
	}
	token := new(big.Int)
	if _, ok := token.SetString(parts[2], 10); !ok {
		return cloudevent.ERC721DID{}, fmt.Errorf("invalid token id %q", parts[2])
	}
	return cloudevent.ERC721DID{
		ChainID:         chain.Uint64(),
		ContractAddress: common.HexToAddress(parts[1]),
		TokenID:         token,
	}, nil
}

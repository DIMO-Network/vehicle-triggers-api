// triggers-dlq is an operator tool for the dead-letter stream
// (DIMO_TRIGGER_DLQ). Messages land here after exceeding MaxDeliver retries
// on the signal/event consumers. Each carries headers describing why it
// failed and where it came from.
//
// Subcommands:
//
//	triggers-dlq list                 # summarize every DLQ message
//	triggers-dlq get <seq>            # print one message (headers + body)
//	triggers-dlq replay [--all|<seq>] # republish to the original subject, then drop from DLQ
//	triggers-dlq purge [--yes]        # delete the entire DLQ stream contents
//
// Replay re-publishes to the X-Original-Subject header so the message
// re-enters the normal evaluation path. Use after fixing whatever caused the
// failure (a webhook endpoint that was down, a transient bug).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	nc "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type flags struct {
	URL       string
	CredsFile string
	Stream    string
	DLQPrefix string
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	sub := os.Args[1]
	fs := flag.NewFlagSet(sub, flag.ExitOnError)
	f := flags{}
	fs.StringVar(&f.URL, "url", "nats://localhost:4222", "NATS URL")
	fs.StringVar(&f.CredsFile, "creds", "", "NATS credentials file (optional)")
	fs.StringVar(&f.Stream, "stream", "DIMO_TRIGGER_DLQ", "DLQ stream name")
	fs.StringVar(&f.DLQPrefix, "dlq-prefix", "dimo.dlq.", "DLQ subject prefix (stripped on replay if X-Original-Subject absent)")

	all := fs.Bool("all", false, "replay: replay every message")
	yes := fs.Bool("yes", false, "purge: skip confirmation")

	switch sub {
	case "list":
		_ = fs.Parse(os.Args[2:])
		mustRun(cmdList(f))
	case "get":
		_ = fs.Parse(os.Args[2:])
		if fs.NArg() < 1 {
			fmt.Fprintln(os.Stderr, "usage: triggers-dlq get <seq>")
			os.Exit(2)
		}
		mustRun(cmdGet(f, fs.Arg(0)))
	case "replay":
		_ = fs.Parse(os.Args[2:])
		mustRun(cmdReplay(f, *all, fs.Args()))
	case "purge":
		_ = fs.Parse(os.Args[2:])
		mustRun(cmdPurge(f, *yes))
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: triggers-dlq {list|get <seq>|replay [--all|<seq>...]|purge [--yes]} [flags]")
}

func mustRun(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func open(f flags) (*nc.Conn, jetstream.JetStream, jetstream.Stream, error) {
	opts := []nc.Option{nc.Name("triggers-dlq"), nc.Timeout(5 * time.Second)}
	if f.CredsFile != "" {
		opts = append(opts, nc.UserCredentials(f.CredsFile))
	}
	conn, err := nc.Connect(f.URL, opts...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("nats connect: %w", err)
	}
	js, err := jetstream.New(conn)
	if err != nil {
		conn.Close()
		return nil, nil, nil, fmt.Errorf("jetstream: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := js.Stream(ctx, f.Stream)
	if err != nil {
		conn.Close()
		return nil, nil, nil, fmt.Errorf("stream %q: %w", f.Stream, err)
	}
	return conn, js, stream, nil
}

// eachMessage walks the stream from seq 1 using a short-lived ordered consumer
// and calls fn for each message. Stops at the current last sequence so it
// terminates on a live stream.
func eachMessage(ctx context.Context, stream jetstream.Stream, fn func(jetstream.Msg) error) error {
	info, err := stream.Info(ctx)
	if err != nil {
		return fmt.Errorf("stream info: %w", err)
	}
	last := info.State.LastSeq
	if info.State.Msgs == 0 || last == 0 {
		return nil
	}
	cons, err := stream.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{})
	if err != nil {
		return fmt.Errorf("ordered consumer: %w", err)
	}
	for {
		msg, err := cons.Next()
		if err != nil {
			return fmt.Errorf("next: %w", err)
		}
		if err := fn(msg); err != nil {
			return err
		}
		meta, err := msg.Metadata()
		if err == nil && meta.Sequence.Stream >= last {
			return nil
		}
	}
}

func cmdList(f flags) error {
	conn, _, stream, err := open(f)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	count := 0
	err = eachMessage(ctx, stream, func(m jetstream.Msg) error {
		meta, _ := m.Metadata()
		seq := uint64(0)
		var ts time.Time
		if meta != nil {
			seq = meta.Sequence.Stream
			ts = meta.Timestamp
		}
		fmt.Printf("seq=%-6d subject=%s original=%q reason=%q delivered=%s at=%s\n",
			seq, m.Subject(),
			m.Headers().Get("X-Original-Subject"),
			truncate(m.Headers().Get("X-Failure-Reason"), 80),
			m.Headers().Get("X-Delivered-Count"),
			ts.Format(time.RFC3339))
		count++
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("\n%d message(s) in %s\n", count, f.Stream)
	return nil
}

func cmdGet(f flags, seqStr string) error {
	seq, err := strconv.ParseUint(seqStr, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid seq %q: %w", seqStr, err)
	}
	conn, _, stream, err := open(f)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := stream.GetMsg(ctx, seq)
	if err != nil {
		return fmt.Errorf("get seq %d: %w", seq, err)
	}
	fmt.Printf("seq:     %d\n", raw.Sequence)
	fmt.Printf("subject: %s\n", raw.Subject)
	fmt.Printf("time:    %s\n", raw.Time.Format(time.RFC3339))
	fmt.Println("headers:")
	for k, vs := range raw.Header {
		for _, v := range vs {
			fmt.Printf("  %s: %s\n", k, v)
		}
	}
	fmt.Printf("body:\n%s\n", string(raw.Data))
	return nil
}

func cmdReplay(f flags, all bool, args []string) error {
	conn, js, stream, err := open(f)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	replayOne := func(raw *jetstream.RawStreamMsg) error {
		original := ""
		if raw.Header != nil {
			original = raw.Header.Get("X-Original-Subject")
		}
		if original == "" {
			return fmt.Errorf("seq %d has no X-Original-Subject header; cannot replay", raw.Sequence)
		}
		out := &nc.Msg{Subject: original, Data: raw.Data}
		if _, err := js.PublishMsg(ctx, out); err != nil {
			return fmt.Errorf("republish seq %d to %q: %w", raw.Sequence, original, err)
		}
		if err := stream.DeleteMsg(ctx, raw.Sequence); err != nil {
			return fmt.Errorf("delete seq %d after replay: %w", raw.Sequence, err)
		}
		fmt.Printf("replayed seq=%d -> %s\n", raw.Sequence, original)
		return nil
	}

	if all {
		// Snapshot sequences first so deletes don't disturb iteration.
		var seqs []uint64
		if err := eachMessage(ctx, stream, func(m jetstream.Msg) error {
			if meta, err := m.Metadata(); err == nil {
				seqs = append(seqs, meta.Sequence.Stream)
			}
			return nil
		}); err != nil {
			return err
		}
		for _, seq := range seqs {
			raw, err := stream.GetMsg(ctx, seq)
			if err != nil {
				fmt.Fprintf(os.Stderr, "skip seq %d: %v\n", seq, err)
				continue
			}
			if err := replayOne(raw); err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
			}
		}
		return nil
	}

	if len(args) == 0 {
		return errors.New("replay needs --all or one or more <seq> args")
	}
	for _, a := range args {
		seq, err := strconv.ParseUint(a, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid seq %q: %w", a, err)
		}
		raw, err := stream.GetMsg(ctx, seq)
		if err != nil {
			return fmt.Errorf("get seq %d: %w", seq, err)
		}
		if err := replayOne(raw); err != nil {
			return err
		}
	}
	return nil
}

func cmdPurge(f flags, yes bool) error {
	if !yes {
		fmt.Fprintln(os.Stderr, "refusing to purge without --yes (this deletes all DLQ messages)")
		os.Exit(2)
	}
	conn, _, stream, err := open(f)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := stream.Purge(ctx); err != nil {
		return fmt.Errorf("purge: %w", err)
	}
	fmt.Printf("purged %s\n", f.Stream)
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Package nats provides a thin wrapper around nats.go + JetStream for the
// vehicle-triggers-api service. It exposes a Client that owns the connection,
// a JetStream context, and KV buckets, plus helpers for publish / pull-consume
// and declarative stream+consumer+bucket provisioning.
package nats

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/DIMO-Network/vehicle-triggers-api/internal/config"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/rs/zerolog"
)

// Client owns a NATS connection and JetStream context.
type Client struct {
	Conn *nats.Conn
	JS   jetstream.JetStream

	cfg config.NATSSettings
	log zerolog.Logger
}

// Connect establishes a NATS connection and initializes JetStream.
// The caller owns Close().
func Connect(ctx context.Context, cfg config.NATSSettings, log zerolog.Logger) (*Client, error) {
	opts := []nats.Option{
		nats.Name(cfg.Name),
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(time.Second),
		nats.Timeout(10 * time.Second),
		nats.DrainTimeout(30 * time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			log.Warn().Err(err).Msg("nats disconnected")
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			log.Info().Str("url", c.ConnectedUrl()).Msg("nats reconnected")
		}),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			log.Error().Err(err).Msg("nats async error")
		}),
	}
	if cfg.CredsFile != "" {
		opts = append(opts, nats.UserCredentials(cfg.CredsFile))
	}

	nc, err := nats.Connect(cfg.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}

	jsOpts := []jetstream.JetStreamOpt{}
	if cfg.PublishAsyncMaxPending > 0 {
		jsOpts = append(jsOpts, jetstream.WithPublishAsyncMaxPending(cfg.PublishAsyncMaxPending))
	}
	js, err := jetstream.New(nc, jsOpts...)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream init: %w", err)
	}

	// Sanity check JetStream reachability with the supplied context.
	if _, err := js.AccountInfo(ctx); err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream account info: %w", err)
	}

	return &Client{Conn: nc, JS: js, cfg: cfg, log: log}, nil
}

// Close drains the NATS connection. Prefer Shutdown when the caller has issued
// async publishes that need to flush first.
func (c *Client) Close() error {
	if c == nil || c.Conn == nil {
		return nil
	}
	if err := c.Conn.Drain(); err != nil && !errors.Is(err, nats.ErrConnectionClosed) {
		return err
	}
	return nil
}

// Shutdown waits for any in-flight async publishes to be acked (bounded by
// ctx), then drains the NATS connection. Call this on service stop.
func (c *Client) Shutdown(ctx context.Context) error {
	if c == nil || c.Conn == nil {
		return nil
	}
	if c.JS != nil {
		select {
		case <-c.JS.PublishAsyncComplete():
		case <-ctx.Done():
			c.log.Warn().Err(ctx.Err()).Msg("nats shutdown: async publishes did not complete before deadline")
		}
	}
	return c.Close()
}

// Config returns the settings the client was constructed with.
func (c *Client) Config() config.NATSSettings { return c.cfg }

// Healthy reports whether the underlying NATS connection is currently
// connected. Reconnect-in-flight returns false so /health surfaces transient
// disconnects to liveness probes.
func (c *Client) Healthy() bool {
	if c == nil || c.Conn == nil {
		return false
	}
	return c.Conn.Status() == nats.CONNECTED
}

// StreamHealth probes each of the configured streams and returns a per-stream
// status. A stream is "ok" when it exists and reports a current leader. A
// stream that's read-only or has lost replicas surfaces as an error string
// in the returned map. Used by /health so a degraded JetStream doesn't pass
// liveness while accepting reads but refusing writes.
//
// Probes run in parallel and the whole call is capped at 1s so /health
// always returns well within the kube probe timeout, even when one stream
// is wedged.
func (c *Client) StreamHealth(ctx context.Context) map[string]string {
	if c == nil || c.JS == nil {
		return map[string]string{"_client": "nil"}
	}
	names := []string{
		c.cfg.SignalsStream,
		c.cfg.EventsStream,
		c.cfg.AuditStream,
		c.cfg.DLQStream,
	}

	probeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	type result struct {
		name, status string
	}
	results := make(chan result, len(names))
	var wg sync.WaitGroup
	for _, name := range names {
		if name == "" {
			continue
		}
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			stream, err := c.JS.Stream(probeCtx, n)
			if err != nil {
				results <- result{n, "stream lookup failed: " + err.Error()}
				return
			}
			info, err := stream.Info(probeCtx)
			if err != nil {
				results <- result{n, "stream info failed: " + err.Error()}
				return
			}
			if info.Cluster != nil && info.Cluster.Leader == "" {
				results <- result{n, "no leader"}
				return
			}
			results <- result{n, "ok"}
		}(name)
	}
	wg.Wait()
	close(results)

	out := make(map[string]string, len(names))
	for r := range results {
		out[r.name] = r.status
	}
	return out
}

// StreamsOK reports whether every probed stream is healthy.
func (c *Client) StreamsOK(ctx context.Context) bool {
	for _, status := range c.StreamHealth(ctx) {
		if status != "ok" {
			return false
		}
	}
	return true
}

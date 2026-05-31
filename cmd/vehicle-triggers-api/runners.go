package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/DIMO-Network/server-garage/pkg/runner"
	vtnats "github.com/DIMO-Network/vehicle-triggers-api/internal/nats"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"
)

// supervised runs `start` in the errgroup with structured enter/exit logs and
// optional graceful `stop` once ctx cancels. Replaces the duplicated
// run* helpers main.go used to carry. All shared the same shape: log enter ->
// start -> log exit -> wrap error with the subsystem name; only the inner
// call differed.
//
// The `stop` callback may be nil for subsystems that exit on ctx alone (NATS
// pull loop). When non-nil it runs in a parallel goroutine wired to
// ctx.Done(); its error joins the group like any other.
func supervised(
	ctx context.Context,
	group *errgroup.Group,
	logger *zerolog.Logger,
	name string,
	fields map[string]string,
	start func(ctx context.Context) error,
	stop func(ctx context.Context) error,
) {
	group.Go(func() error {
		log := logger.With().Str("subsystem", name).Logger()
		for k, v := range fields {
			log = log.With().Str(k, v).Logger()
		}
		log.Info().Msg("supervisor: start enter")
		err := start(ctx)
		log.Info().Err(err).Msg("supervisor: start exit")
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("%s start: %w", name, err)
		}
		return nil
	})
	if stop == nil {
		return
	}
	group.Go(func() error {
		<-ctx.Done()
		log := logger.With().Str("subsystem", name).Logger()
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		log.Info().Msg("supervisor: stop enter")
		err := stop(stopCtx)
		log.Info().Err(err).Msg("supervisor: stop exit")
		if err != nil {
			return fmt.Errorf("%s stop: %w", name, err)
		}
		return nil
	})
}

// runNATSPullLoop wires a JetStream pull loop through the supervisor.
func runNATSPullLoop(ctx context.Context, group *errgroup.Group, logger *zerolog.Logger, name string, client *vtnats.Client, cons jetstream.Consumer, handler vtnats.PayloadHandler, maxInFlight int) {
	supervised(ctx, group, logger, name, nil,
		func(ctx context.Context) error {
			return client.PullLoop(ctx, cons, maxInFlight, handler)
		},
		nil,
	)
}

// runFiber wires a Fiber app's Listen/Shutdown pair through the supervisor.
func runFiber(ctx context.Context, group *errgroup.Group, logger *zerolog.Logger, fiberApp runner.FiberApp, addr string) {
	supervised(ctx, group, logger, "fiber", map[string]string{"addr": addr},
		func(_ context.Context) error { return fiberApp.Listen(addr) },
		func(_ context.Context) error { return fiberApp.Shutdown() },
	)
}

// runHTTPServer wires an http.Handler through the supervisor as an
// http.Server. Used for the monitoring server.
func runHTTPServer(ctx context.Context, group *errgroup.Group, logger *zerolog.Logger, name string, handler http.Handler, addr string) {
	srv := &http.Server{Addr: addr, Handler: handler}
	supervised(ctx, group, logger, name, map[string]string{"addr": addr},
		func(_ context.Context) error { return srv.ListenAndServe() },
		func(stopCtx context.Context) error { return srv.Shutdown(stopCtx) },
	)
}

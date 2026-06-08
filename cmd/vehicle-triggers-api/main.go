package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/DIMO-Network/server-garage/pkg/env"
	"github.com/DIMO-Network/server-garage/pkg/logging"
	"github.com/DIMO-Network/server-garage/pkg/monserver"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/app"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/config"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/migrations"
	vtnats "github.com/DIMO-Network/vehicle-triggers-api/internal/nats"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"
)

// @title           Vehicle Triggers API
// @version         1.0
//
// @securityDefinitions.apikey BearerAuth
// @in                         header
// @name                       Authorization
// @description                JWT Authorization header using the Bearer scheme. Example: "Bearer {token}"
//
// @BasePath  /
func main() {
	logger := logging.GetAndSetDefaultLogger("vehicle-triggers-api")
	mainCtx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-mainCtx.Done()
		logger.Info().Msg("Received signal, shutting down...")
		cancel()
	}()

	runnerGroup, runnerCtx := errgroup.WithContext(mainCtx)

	migrationCommand := flag.String("migrations", "", "run migrations")
	envFile := flag.String("env-file", ".env", "path to env file")
	migrateOnly := flag.Bool("migrate-only", false, "run migrations only")
	flag.Parse()

	settings, err := env.LoadSettings[config.Settings](*envFile)
	if err != nil {
		log.Fatalf("could not load settings: %s", err)
	}

	if settings.LogLevel == "" {
		settings.LogLevel = "info"
	}
	level, err := zerolog.ParseLevel(settings.LogLevel)
	if err != nil {
		log.Fatalf("could not parse log level: %s", err)
	}
	zerolog.SetGlobalLevel(level)
	logger = logging.GetAndSetDefaultLogger(settings.ServiceName)

	if *migrationCommand != "" || *migrateOnly {
		if *migrationCommand == "" {
			*migrationCommand = "up -v"
		}
		if err := migrations.RunGoose(mainCtx, strings.Fields(*migrationCommand), settings.DB); err != nil {
			logger.Fatal().Err(err).Msg("Failed to run migrations")
		}
		if *migrateOnly {
			return
		}
	}

	// Migrations don't need full config; validate everything else only
	// when we're about to start the service.
	if err := settings.Validate(); err != nil {
		log.Fatalf("settings.Validate: %s", err)
	}

	monApp := monserver.NewMonitoringServer(&logger, settings.EnablePprof)
	logger.Info().Str("port", strconv.Itoa(settings.MonPort)).Msgf("Starting monitoring server")
	runHTTPServer(runnerCtx, runnerGroup, &logger, "monitoring", monApp, net.JoinHostPort("0.0.0.0", strconv.Itoa(settings.MonPort)))

	servers, err := app.CreateServers(runnerCtx, &settings, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to create servers")
	}
	logger.Info().Str("port", strconv.Itoa(settings.Port)).Msgf("Starting web server")
	runFiber(runnerCtx, runnerGroup, &logger, servers.Application, net.JoinHostPort("0.0.0.0", strconv.Itoa(settings.Port)))

	// Phased shutdown for the ingest -> dispatch -> NATS pipeline. A webhook is
	// acked off JetStream as soon as it is enqueued (async dispatch), so an
	// unordered teardown would drop acked-but-unsent webhooks on every deploy.
	// We tear the pipeline down strictly in dependency order:
	//   1. signal cancels runnerCtx        -> pull loops stop pulling/acking and
	//                                          drain their in-flight handlers
	//   2. once consumers have exited       -> stop the dispatcher; its workers
	//                                          drain the queue (no new Enqueue can
	//                                          race them now)
	//   3. once the dispatcher has drained  -> stop the audit queue; it flushes
	//                                          the dispatcher's final audit records
	//   4. once audit has drained           -> close the NATS connection last so
	//                                          RecordFire / audit publishes landed
	if servers.NATSClient != nil {
		client := servers.NATSClient
		dispatcherCtx, stopDispatcher := context.WithCancel(context.Background())
		auditCtx, stopAudit := context.WithCancel(context.Background())
		var dispatcherWG, auditWG, consumerWG sync.WaitGroup

		if servers.AuditQueue != nil {
			aq := servers.AuditQueue
			auditWG.Go(func() {
				logger.Info().Msg("audit queue: starting drainer pool")
				err := aq.Run(auditCtx)
				logger.Info().Err(err).Msg("audit queue: drainer pool exited")
			})
		}
		if servers.Dispatcher != nil {
			disp := servers.Dispatcher
			dispatcherWG.Go(func() {
				logger.Info().Msg("dispatcher: starting workers")
				err := disp.Run(dispatcherCtx)
				logger.Info().Err(err).Msg("dispatcher: workers exited")
			})
		}

		startPullLoop := func(name string, cons jetstream.Consumer, handler vtnats.PayloadHandler) {
			consumerWG.Add(1)
			runnerGroup.Go(func() error {
				defer consumerWG.Done()
				slog := logger.With().Str("subsystem", name).Logger()
				slog.Info().Msg("pull loop: start")
				err := client.PullLoop(runnerCtx, cons, settings.MaxInFlight, handler)
				slog.Info().Err(err).Msg("pull loop: exit")
				if err != nil && !errors.Is(err, context.Canceled) {
					return fmt.Errorf("%s: %w", name, err)
				}
				return nil
			})
		}
		if servers.NATSSignalConsumer != nil {
			startPullLoop("nats-signals", servers.NATSSignalConsumer, servers.NATSListener.HandleSignalPayload)
		}
		if servers.NATSEventConsumer != nil {
			startPullLoop("nats-events", servers.NATSEventConsumer, servers.NATSListener.HandleEventPayload)
		}

		// Shutdown coordinator: the single errgroup member that owns the
		// ordered teardown. It blocks the group from returning until every
		// phase completes.
		runnerGroup.Go(func() error {
			<-runnerCtx.Done()
			consumerWG.Wait() // phase 1: consumers + in-flight handlers done
			logger.Info().Msg("shutdown: consumers drained; stopping dispatcher")
			stopDispatcher() // phase 2
			dispatcherWG.Wait()
			logger.Info().Msg("shutdown: dispatcher drained; stopping audit queue")
			stopAudit() // phase 3
			auditWG.Wait()
			logger.Info().Msg("shutdown: audit drained; closing NATS")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := client.Shutdown(shutdownCtx); err != nil { // phase 4
				return fmt.Errorf("nats shutdown: %w", err)
			}
			return nil
		})
	}

	if err := runnerGroup.Wait(); err != nil {
		logger.Fatal().Err(err).Msg("Server failed.")
	}
	logger.Info().Msg("Server stopped.")
}

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/DIMO-Network/server-garage/pkg/env"
	"github.com/DIMO-Network/server-garage/pkg/logging"
	"github.com/DIMO-Network/server-garage/pkg/monserver"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/app"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/config"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/migrations"
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

	if servers.AuditQueue != nil {
		aq := servers.AuditQueue
		runnerGroup.Go(func() error {
			logger.Info().Msg("audit queue: starting drainer pool")
			err := aq.Run(runnerCtx)
			logger.Info().Err(err).Msg("audit queue: drainer pool exited")
			return err
		})
	}
	if servers.Dispatcher != nil {
		disp := servers.Dispatcher
		runnerGroup.Go(func() error {
			logger.Info().Msg("dispatcher: starting workers")
			err := disp.Run(runnerCtx)
			logger.Info().Err(err).Msg("dispatcher: workers exited")
			return err
		})
	}
	if servers.NATSSignalConsumer != nil {
		runNATSPullLoop(runnerCtx, runnerGroup, &logger, "nats-signals", servers.NATSClient, servers.NATSSignalConsumer, servers.NATSListener.HandleSignalPayload, settings.MaxInFlight)
	}
	if servers.NATSEventConsumer != nil {
		runNATSPullLoop(runnerCtx, runnerGroup, &logger, "nats-events", servers.NATSClient, servers.NATSEventConsumer, servers.NATSListener.HandleEventPayload, settings.MaxInFlight)
	}
	if servers.NATSClient != nil {
		client := servers.NATSClient
		runnerGroup.Go(func() error {
			<-runnerCtx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := client.Shutdown(shutdownCtx); err != nil {
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

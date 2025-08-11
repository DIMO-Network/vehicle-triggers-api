package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/DIMO-Network/server-garage/pkg/env"
	"github.com/DIMO-Network/server-garage/pkg/logging"
	"github.com/DIMO-Network/server-garage/pkg/monserver"
	"github.com/DIMO-Network/server-garage/pkg/runner"
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
		err := migrations.RunGoose(mainCtx, strings.Fields(*migrationCommand), settings.DB)
		if err != nil {
			logger.Fatal().Err(err).Msg("Failed to run migrations")
		}
		if *migrateOnly {
			return
		}
	}

	monApp := monserver.NewMonitoringServer(&logger, settings.EnablePprof)
	logger.Info().Str("port", strconv.Itoa(settings.MonPort)).Msgf("Starting monitoring server")
	runner.RunHandler(runnerCtx, runnerGroup, monApp, ":"+strconv.Itoa(settings.MonPort))

	app, err := app.CreateServers(runnerCtx, &settings, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to create servers")
	}
	logger.Info().Str("port", strconv.Itoa(settings.Port)).Msgf("Starting web server")
	runner.RunFiber(runnerCtx, runnerGroup, app, ":"+strconv.Itoa(settings.Port))

	if err := runnerGroup.Wait(); err != nil {
		logger.Fatal().Err(err).Msg("Server failed.")
	}
	logger.Info().Msg("Server stopped.")
}

package main

import (
	"context"
	"log"
	"os"
	"strings"

	"github.com/DIMO-Network/shared"
	"github.com/DIMO-Network/shared/db"
	"github.com/DIMO-Network/vehicle-events-api/internal/api"
	"github.com/DIMO-Network/vehicle-events-api/internal/config"
	"github.com/pressly/goose/v3"
	"github.com/rs/zerolog"
)

func MigrateDatabase(ctx context.Context, logger zerolog.Logger, s *config.Settings, args []string) {
	command := "up"
	if len(args) > 2 {
		command = args[2]
		if command == "down-to" || command == "up-to" {
			command = command + " " + args[3]
		}
	}

	sqlDb := db.NewDbConnectionFromSettings(ctx, &s.DB, true)
	sqlDb.WaitForDB(logger)

	if command == "" {
		command = "up"
	}

	_, err := sqlDb.DBS().Writer.Exec("CREATE SCHEMA IF NOT EXISTS vehicle_events_api;")
	if err != nil {
		logger.Fatal().Err(err).Msg("could not create schema:")
	}
	goose.SetTableName("vehicle_events_api.migrations")
	if err := goose.RunContext(ctx, command, sqlDb.DBS().Writer.DB, "internal/infrastructure/db/migrations"); err != nil {
		logger.Fatal().Err(err).Msg("failed to apply migrations")
	}
}

func main() {
	gitSha1 := os.Getenv("GIT_SHA1")
	ctx := context.Background()

	// Load settings
	settings, err := shared.LoadConfig[config.Settings]("settings.yaml")
	if err != nil {
		log.Fatalf("could not load settings: %s", err)
	}

	// Configure logger
	level, err := zerolog.ParseLevel(settings.LogLevel)
	if err != nil {
		log.Fatalf("could not parse log level: %s", err)
	}
	logger := zerolog.New(os.Stdout).Level(level).With().
		Timestamp().
		Str("app", settings.ServiceName).
		Str("git-sha1", gitSha1).
		Logger()

	// Check if migration is requested
	args := os.Args
	if len(args) > 1 && strings.ToLower(args[1]) == "migrate" {
		MigrateDatabase(ctx, logger, &settings, args)
		return
	}

	// Create DB connection
	store := db.NewDbConnectionFromSettings(ctx, &settings.DB, true)
	store.WaitForDB(logger)

	// Start API server
	api.Run(ctx, logger, store)
}

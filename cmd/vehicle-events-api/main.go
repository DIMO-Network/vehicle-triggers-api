// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
package main

import (
	"context"
	"log"
	"os"
	"strings"

	"github.com/DIMO-Network/shared"
	sharedDB "github.com/DIMO-Network/shared/db"

	"github.com/DIMO-Network/vehicle-events-api/internal/api"
	"github.com/DIMO-Network/vehicle-events-api/internal/config"
	"github.com/DIMO-Network/vehicle-events-api/internal/db"
	"github.com/rs/zerolog"
)

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
		db.MigrateDatabase(ctx, logger, &settings, args)
		return
	}

	// Create DB connection
	store := sharedDB.NewDbConnectionFromSettings(ctx, &settings.DB, true)
	store.WaitForDB(logger)

	// Start API server
	api.Run(ctx, logger, store)
}

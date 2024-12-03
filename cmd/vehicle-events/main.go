package main

import (
	"context"
	"github.com/DIMO-Network/vehicle-events-api/internal/api"
	"log"
	"os"

	"github.com/DIMO-Network/shared"
	"github.com/DIMO-Network/shared/db"
	"github.com/DIMO-Network/vehicle-events-api/internal/config"
	"github.com/rs/zerolog"
)

// @title           Vehicle Events Service API
// @version         1.0
// @description     API for managing vehicle events and webhooks
// @termsOfService  http://swagger.io/terms/

// @contact.name   Support Team
// @contact.url    http://www.swagger.io/support
// @contact.email  support@swagger.io

// @license.name   Apache 2.0
// @license.url    http://www.apache.org/licenses/LICENSE-2.0.html

// @host      localhost:8080
// @BasePath  /

// @securityDefinitions.apikey  ApiKeyAuth
// @in                          header
// @name                        Authorization

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

	store := db.NewDbConnectionFromSettings(ctx, &settings.DB, true)

	store.WaitForDB(logger)

	api.Run(ctx, logger, store)

}

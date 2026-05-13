package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/DIMO-Network/server-garage/pkg/env"
	"github.com/DIMO-Network/server-garage/pkg/logging"
	"github.com/DIMO-Network/server-garage/pkg/monserver"
	"github.com/DIMO-Network/server-garage/pkg/runner"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/app"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/config"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/db/migrations"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/kafka"
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
	runHandlerWithLogging(runnerCtx, runnerGroup, &logger, "monitoring", monApp, net.JoinHostPort("0.0.0.0", strconv.Itoa(settings.MonPort)))

	servers, err := app.CreateServers(runnerCtx, &settings, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to create servers")
	}
	logger.Info().Str("port", strconv.Itoa(settings.Port)).Msgf("Starting web server")
	runFiberWithLogging(runnerCtx, runnerGroup, &logger, servers.Application, net.JoinHostPort("0.0.0.0", strconv.Itoa(settings.Port)))
	RunConsumer(runnerCtx, runnerGroup, &logger, servers.SignalConsumer)
	RunConsumer(runnerCtx, runnerGroup, &logger, servers.EventConsumer)

	if err := runnerGroup.Wait(); err != nil {
		logger.Fatal().Err(err).Msg("Server failed.")
	}
	logger.Info().Msg("Server stopped.")
}

// RunConsumer starts the Kafka consumer in a single goroutine. Stop is
// deferred until Start returns so the subscriber cannot be closed before
// Subscribe runs (which would mask the real Subscribe error as
// "subscriber closed"). Entry/exit is logged with the consumer's name.
func RunConsumer(ctx context.Context, group *errgroup.Group, logger *zerolog.Logger, consumer *kafka.Consumer) {
	name := consumer.Name()
	topic := consumer.Topic()
	group.Go(func() error {
		logger.Info().Str("consumer", name).Str("topic", topic).Msg("consumer goroutine: start enter")
		err := consumer.Start(ctx)
		logger.Info().Str("consumer", name).Str("topic", topic).Err(err).Msg("consumer goroutine: start exit")

		stopErr := consumer.Stop(context.Background())
		logger.Info().Str("consumer", name).Str("topic", topic).Err(stopErr).Msg("consumer goroutine: stop exit")

		if err != nil {
			return fmt.Errorf("consumer %q (topic %q) start: %w", name, topic, err)
		}
		if stopErr != nil {
			return fmt.Errorf("consumer %q (topic %q) stop: %w", name, topic, stopErr)
		}
		return nil
	})
}

// runFiberWithLogging mirrors runner.RunFiber but logs goroutine
// enter/exit so we can see which subsystem returned first.
func runFiberWithLogging(ctx context.Context, group *errgroup.Group, logger *zerolog.Logger, fiberApp runner.FiberApp, addr string) {
	group.Go(func() error {
		logger.Info().Str("addr", addr).Msg("fiber goroutine: listen enter")
		err := fiberApp.Listen(addr)
		logger.Info().Str("addr", addr).Err(err).Msg("fiber goroutine: listen exit")
		if err != nil {
			return fmt.Errorf("fiber listen %q: %w", addr, err)
		}
		return nil
	})
	group.Go(func() error {
		<-ctx.Done()
		logger.Info().Str("addr", addr).Msg("fiber goroutine: shutdown enter")
		err := fiberApp.Shutdown()
		logger.Info().Str("addr", addr).Err(err).Msg("fiber goroutine: shutdown exit")
		if err != nil {
			return fmt.Errorf("fiber shutdown %q: %w", addr, err)
		}
		return nil
	})
}

// runHandlerWithLogging mirrors runner.RunHandler but logs goroutine
// enter/exit so we can see which subsystem returned first.
func runHandlerWithLogging(ctx context.Context, group *errgroup.Group, logger *zerolog.Logger, name string, handler http.Handler, addr string) {
	srv := &http.Server{Addr: addr, Handler: handler}
	group.Go(func() error {
		logger.Info().Str("server", name).Str("addr", addr).Msg("http goroutine: listen enter")
		err := srv.ListenAndServe()
		logger.Info().Str("server", name).Str("addr", addr).Err(err).Msg("http goroutine: listen exit")
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("%s listen %q: %w", name, addr, err)
		}
		return nil
	})
	group.Go(func() error {
		<-ctx.Done()
		logger.Info().Str("server", name).Str("addr", addr).Msg("http goroutine: shutdown enter")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := srv.Shutdown(shutdownCtx)
		logger.Info().Str("server", name).Str("addr", addr).Err(err).Msg("http goroutine: shutdown exit")
		if err != nil {
			return fmt.Errorf("%s shutdown %q: %w", name, addr, err)
		}
		return nil
	})
}

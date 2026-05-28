package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/DIMO-Network/server-garage/pkg/fibercommon"
	"github.com/DIMO-Network/shared/pkg/db"
	_ "github.com/DIMO-Network/vehicle-triggers-api/docs" // Import Swagger docs
	"github.com/DIMO-Network/vehicle-triggers-api/internal/auth"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/clients/identity"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/clients/tokenexchange"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/config"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/metriclistener"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/webhook"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/kafka"
	vtnats "github.com/DIMO-Network/vehicle-triggers-api/internal/nats"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggerevaluator"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/auditqueue"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/cachebroadcast"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggerstate"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhookcache"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhookdispatcher"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhooksender"
	"github.com/IBM/sarama"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/redirect"
	"github.com/gofiber/swagger"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/rs/zerolog"
)

type Servers struct {
	Application    *fiber.App
	SignalConsumer *kafka.Consumer
	EventConsumer  *kafka.Consumer

	// NATSClient is non-nil when NATSSettings.Enabled(). main owns shutdown.
	NATSClient *vtnats.Client
	// NATSSignalConsumer / NATSEventConsumer are non-nil when NATS owns the
	// evaluation path (NATSSettings.PrimaryMode()).
	NATSSignalConsumer jetstream.Consumer
	NATSEventConsumer  jetstream.Consumer
	// NATSListener is the listener used to dispatch messages pulled off the
	// NATS consumers.
	NATSListener *metriclistener.MetricListener
	// Dispatcher decouples webhook delivery from the JetStream handler. Non-
	// nil whenever NATS owns the evaluation path; main is responsible for
	// calling Run on it in the errgroup.
	Dispatcher *webhookdispatcher.Dispatcher
	// AuditQueue fronts the audit stream with a fire-and-forget pool. Same
	// lifecycle as the dispatcher.
	AuditQueue *auditqueue.Queue
}

func CreateServers(ctx context.Context, settings *config.Settings, logger zerolog.Logger) (*Servers, error) {
	store := db.NewDbConnectionFromSettings(ctx, &settings.DB, true)
	store.WaitForDB(logger)

	tokenExchangeAPI, err := tokenexchange.New(settings)
	if err != nil {
		return nil, fmt.Errorf("failed to create token exchange API: %w", err)
	}
	tokenExchangeCache := tokenexchange.NewCache(settings.TokenExchangeCacheExpiration, settings.TokenExchangeCacheCleanupInterval, tokenExchangeAPI)

	repo := triggersrepo.NewRepository(store.DBS().Writer.DB)

	webhookCache, err := startWebhookCache(ctx, settings, tokenExchangeCache, repo)
	if err != nil {
		return nil, fmt.Errorf("failed to start webhook cache: %w", err)
	}

	var (
		natsClient   *vtnats.Client
		natsListener *metriclistener.MetricListener
		natsSigCons  jetstream.Consumer
		natsEvtCons  jetstream.Consumer
		bridge       metriclistener.NATSBridge
		dispatcher   *webhookdispatcher.Dispatcher
		auditQ       *auditqueue.Queue
	)
	if settings.NATS.Enabled() {
		natsClient, err = vtnats.Connect(ctx, settings.NATS, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to nats: %w", err)
		}
		if err := natsClient.EnsureStreams(ctx); err != nil {
			return nil, fmt.Errorf("failed to ensure nats streams: %w", err)
		}
		if err := natsClient.EnsureBuckets(ctx); err != nil {
			return nil, fmt.Errorf("failed to ensure nats buckets: %w", err)
		}
		// Wire the webhook cache to publish + subscribe to invalidation
		// notifications so cross-replica CRUD propagation drops from 5 min
		// (poll) to sub-second.
		webhookCache.SetNotifier(cachebroadcast.NewNATSNotifier(natsClient.Conn, logger))
		if _, err := cachebroadcast.Subscribe(natsClient.Conn, ctx, webhookCache, logger); err != nil {
			return nil, fmt.Errorf("failed to subscribe to cache invalidate: %w", err)
		}
		if settings.NATS.PrimaryMode() {
			bridge = natsClient
			stateKV, err := natsClient.TriggerState(ctx)
			if err != nil {
				return nil, fmt.Errorf("failed to open trigger-state bucket: %w", err)
			}
			historyKV, err := natsClient.SignalHistory(ctx)
			if err != nil {
				return nil, fmt.Errorf("failed to open signal-history bucket: %w", err)
			}
			stateStore := triggerstate.New(stateKV, historyKV)
			webhookSender := webhooksender.NewWebhookSender(nil)
			maxFailureCount := int(settings.MaxWebhookFailureCount)
			if maxFailureCount < 1 {
				maxFailureCount = 1
			}
			auditQ = auditqueue.New(auditqueue.Config{
				Workers: settings.NATS.AuditWorkers,
				Buffer:  settings.NATS.AuditQueueSize,
			}, natsClient, logger)
			dispatcher = webhookdispatcher.New(webhookdispatcher.Config{
				Workers:         settings.NATS.DispatcherWorkers,
				QueueSize:       settings.NATS.DispatcherQueueSize,
				MaxFailureCount: maxFailureCount,
			}, webhookSender, stateStore, auditQ, repo, logger)
			natsListener = buildListenerWithState(settings, tokenExchangeCache, repo, webhookCache, stateStore).
				WithAuditor(natsClient).
				WithStateRecorder(stateStore).
				WithDispatcher(dispatcher)
			natsSigCons, err = natsClient.EnsureConsumer(ctx, vtnats.ConsumerSpec{
				Stream:         settings.NATS.SignalsStream,
				Durable:        settings.NATS.SignalsDurable,
				FilterSubjects: []string{vtnats.AllSignalsFilter()},
				DeliverPolicy:  jetstream.DeliverAllPolicy,
				AckWait:        settings.NATS.AckWait,
				MaxDeliver:     settings.NATS.MaxDeliver,
				MaxAckPending:  settings.NATS.MaxAckPending,
				Description:    "vehicle-triggers signals evaluator",
			})
			if err != nil {
				return nil, fmt.Errorf("failed to ensure signals consumer: %w", err)
			}
			natsEvtCons, err = natsClient.EnsureConsumer(ctx, vtnats.ConsumerSpec{
				Stream:         settings.NATS.EventsStream,
				Durable:        settings.NATS.EventsDurable,
				FilterSubjects: []string{vtnats.AllEventsFilter()},
				DeliverPolicy:  jetstream.DeliverAllPolicy,
				AckWait:        settings.NATS.AckWait,
				MaxDeliver:     settings.NATS.MaxDeliver,
				MaxAckPending:  settings.NATS.MaxAckPending,
				Description:    "vehicle-triggers events evaluator",
			})
			if err != nil {
				return nil, fmt.Errorf("failed to ensure events consumer: %w", err)
			}
		}
	}

	var (
		signalConsumer *kafka.Consumer
		eventConsumer  *kafka.Consumer
	)
	if !settings.NATS.KafkaDisabled() {
		// Kafka path evaluates triggers (or bridges to NATS). When it
		// evaluates, it needs a state store for cooldown / previousValue.
		// Without NATS-primary providing a KV store, fall back to an
		// in-memory store - works for single-replica deployments and tests;
		// real multi-replica setups must enable NATS_MODE=primary.
		var kafkaState *triggerstate.InMemoryStore
		if bridge == nil {
			kafkaState = triggerstate.NewInMemory()
		}
		signalConsumer, err = createSignalConsumer(ctx, settings, tokenExchangeCache, repo, webhookCache, bridge, kafkaState)
		if err != nil {
			return nil, fmt.Errorf("failed to create signal consumer: %w", err)
		}

		eventConsumer, err = createEventConsumer(ctx, settings, tokenExchangeCache, repo, webhookCache, bridge, kafkaState)
		if err != nil {
			return nil, fmt.Errorf("failed to create event consumer: %w", err)
		}
	}

	identityClient, err := identity.New(settings, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create identity client: %w", err)
	}

	app, err := CreateFiberApp(logger, repo, webhookCache, tokenExchangeAPI, identityClient, settings, natsClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create fiber app: %w", err)
	}
	return &Servers{
		Application:        app,
		SignalConsumer:     signalConsumer,
		EventConsumer:      eventConsumer,
		NATSClient:         natsClient,
		NATSSignalConsumer: natsSigCons,
		NATSEventConsumer:  natsEvtCons,
		NATSListener:       natsListener,
		Dispatcher:         dispatcher,
		AuditQueue:         auditQ,
	}, nil
}

// Run sets up the API routes and starts the HTTP server.
func CreateFiberApp(logger zerolog.Logger, repo *triggersrepo.Repository,
	webhookCache *webhookcache.WebhookCache,
	tokenExchangeClient *tokenexchange.Client,
	identityClient *identity.Client,
	settings *config.Settings,
	natsClient *vtnats.Client) (*fiber.App, error) {

	app := fiber.New(fiber.Config{
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			return fibercommon.ErrorHandler(c, err)
		},
		DisableStartupMessage: true,
	})
	app.Use(fibercommon.ContextLoggerMiddleware)

	app.Get("/swagger/*", swagger.HandlerDefault)
	// redirect v1 to v2
	app.Use(redirect.New(redirect.Config{
		Rules: map[string]string{
			"/v1/swagger":   "/swagger",
			"/v1/swagger/*": "/swagger/$1",
		},
		StatusCode: fiber.StatusTemporaryRedirect,
	}))
	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendString("Welcome to the Vehicle Triggers API!")
	})

	// Create a JWT middleware that verifies developer licenses.
	// settings.IdentityAPIURL is loaded from your settings.yaml.

	// Register Webhook routes.
	webhookController, err := webhook.NewWebhookController(repo, webhookCache)
	if err != nil {
		return nil, fmt.Errorf("failed to create webhook controller: %w", err)
	}
	vehicleSubscriptionController := webhook.NewVehicleSubscriptionController(repo, identityClient, tokenExchangeClient, webhookCache)

	app.Get("/health", func(c *fiber.Ctx) error {
		body := fiber.Map{
			"data": "Server is up and running",
		}
		if natsClient != nil {
			ctx, cancel := context.WithTimeout(c.UserContext(), 2*time.Second)
			defer cancel()
			natsHealthy := natsClient.Healthy()
			streams := natsClient.StreamHealth(ctx)
			streamsOK := natsHealthy
			for _, status := range streams {
				if status != "ok" {
					streamsOK = false
				}
			}
			body["nats"] = map[string]any{
				"enabled": true,
				"healthy": natsHealthy,
				"streams": streams,
				"mode":    settings.NATS.Mode,
			}
			if !streamsOK {
				return c.Status(fiber.StatusServiceUnavailable).JSON(body)
			}
		}
		return c.JSON(body)
	})

	jwtMiddleware := auth.Middleware(settings)
	devLicenseMiddleware := auth.NewDevLicenseValidator(identityClient)
	devJWTAuth := app.Use(jwtMiddleware, devLicenseMiddleware)
	// Webhook CRUD
	devJWTAuth.Get("/v1/webhooks", webhookController.ListWebhooks)
	devJWTAuth.Post("/v1/webhooks", webhookController.RegisterWebhook)
	devJWTAuth.Get("/v1/webhooks/signals", webhookController.GetSignalNames)
	devJWTAuth.Get("/v1/webhooks/:webhookId", vehicleSubscriptionController.ListVehiclesForWebhook)
	devJWTAuth.Put("/v1/webhooks/:webhookId", webhookController.UpdateWebhook)
	devJWTAuth.Delete("/v1/webhooks/:webhookId", webhookController.DeleteWebhook)

	// Vehicle subscriptions
	devJWTAuth.Post("/v1/webhooks/:webhookId/subscribe/list", vehicleSubscriptionController.SubscribeVehiclesFromList)
	devJWTAuth.Post("/v1/webhooks/:webhookId/subscribe/all", vehicleSubscriptionController.SubscribeAllVehiclesToWebhook)
	devJWTAuth.Post("/v1/webhooks/:webhookId/subscribe/:assetDID", vehicleSubscriptionController.AssignVehicleToWebhook)
	devJWTAuth.Delete("/v1/webhooks/:webhookId/unsubscribe/list", vehicleSubscriptionController.UnsubscribeVehiclesFromList)
	devJWTAuth.Delete("/v1/webhooks/:webhookId/unsubscribe/all", vehicleSubscriptionController.UnsubscribeAllVehiclesFromWebhook)
	devJWTAuth.Delete("/v1/webhooks/:webhookId/unsubscribe/:assetDID", vehicleSubscriptionController.RemoveVehicleFromWebhook)
	devJWTAuth.Get("/v1/webhooks/vehicles/:assetDID", vehicleSubscriptionController.ListSubscriptions)

	return app, nil
}

// startWebhookCache sets up and starts the Kafka consumer for signals and events.
func startWebhookCache(ctx context.Context, settings *config.Settings, tokenExchangeAPI *tokenexchange.Cache, repo *triggersrepo.Repository) (*webhookcache.WebhookCache, error) {
	// Initialize the in-memory webhook cache.
	webhookCache := webhookcache.NewWebhookCache(repo, settings)

	// load all existing webhooks into memory so GetWebhooks() won't be empty
	if err := webhookCache.PopulateCache(ctx); err != nil {
		return nil, fmt.Errorf("failed to populate webhook cache at startup: %w", err)
	}

	logger := zerolog.Ctx(ctx)
	// Periodically refresh the cache so new/updated webhooks show up without
	// a restart. Each refresh re-scans the full subscriptions table and
	// recompiles every trigger's CEL program, so the interval is set
	// conservatively; subscription writes go through the API which already
	// nudges the cache via ScheduleRefresh.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			webhookCache.ScheduleRefresh(ctx)
		}
	}()

	logger.Info().Msgf("Device signals consumer started on topic: %s", settings.DeviceSignalsTopic)

	return webhookCache, nil
}

// buildListener wires a MetricListener with shared services. Used by both
// the Kafka and NATS sides; the Kafka path optionally wraps it with a bridge.
//
// Deprecated handling: when NATS_MODE=exclusive the Kafka-side listener is
// never constructed, so the bridge variant is only used in the transitional
// NATS_MODE=primary mode.
func buildListener(settings *config.Settings, tokenExchangeCache *tokenexchange.Cache, repo *triggersrepo.Repository, webhookCache *webhookcache.WebhookCache) *metriclistener.MetricListener {
	return buildListenerWithState(settings, tokenExchangeCache, repo, webhookCache, nil)
}

// buildListenerWithState builds a listener whose evaluator consults the
// supplied state store for cooldown. Pass nil for state to fall back to the
// trigger_logs table on every check.
func buildListenerWithState(settings *config.Settings, tokenExchangeCache *tokenexchange.Cache, repo *triggersrepo.Repository, webhookCache *webhookcache.WebhookCache, state triggerevaluator.StateStore) *metriclistener.MetricListener {
	webhookSender := webhooksender.NewWebhookSender(nil)
	evaluator := triggerevaluator.NewTriggerEvaluator(tokenExchangeCache)
	if state != nil {
		evaluator = evaluator.WithStateStore(state)
	}
	return metriclistener.NewMetricsListener(webhookCache, repo, webhookSender, evaluator, settings)
}

func createSignalConsumer(_ context.Context, settings *config.Settings, tokenExchangeCache *tokenexchange.Cache, repo *triggersrepo.Repository, webhookCache *webhookcache.WebhookCache, bridge metriclistener.NATSBridge, state *triggerstate.InMemoryStore) (*kafka.Consumer, error) {
	clusterConfig := sarama.NewConfig()
	clusterConfig.Version = sarama.V2_8_1_0
	clusterConfig.Consumer.Offsets.Initial = sarama.OffsetOldest
	var listener *metriclistener.MetricListener
	if state != nil {
		listener = buildListenerWithState(settings, tokenExchangeCache, repo, webhookCache, state).WithStateRecorder(state)
	} else {
		listener = buildListener(settings, tokenExchangeCache, repo, webhookCache)
	}
	if bridge != nil {
		listener = listener.WithBridge(bridge)
	}
	consumerConfig := &kafka.Config{
		ClusterConfig:   clusterConfig,
		BrokerAddresses: strings.Split(settings.KafkaBrokers, ","),
		Topic:           settings.DeviceSignalsTopic,
		GroupID:         "vehicle-triggers",
		MaxInFlight:     int64(settings.MaxInFlight),
		Processor:       listener.ProcessSignalMessages,
		Name:            "signals",
	}

	consumer, err := kafka.NewConsumer(consumerConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create device signals consumer: %w", err)
	}

	return consumer, nil
}

func createEventConsumer(_ context.Context, settings *config.Settings, tokenExchangeCache *tokenexchange.Cache, repo *triggersrepo.Repository, webhookCache *webhookcache.WebhookCache, bridge metriclistener.NATSBridge, state *triggerstate.InMemoryStore) (*kafka.Consumer, error) {
	clusterConfig := sarama.NewConfig()
	clusterConfig.Version = sarama.V2_8_1_0
	clusterConfig.Consumer.Offsets.Initial = sarama.OffsetOldest
	var listener *metriclistener.MetricListener
	if state != nil {
		listener = buildListenerWithState(settings, tokenExchangeCache, repo, webhookCache, state).WithStateRecorder(state)
	} else {
		listener = buildListener(settings, tokenExchangeCache, repo, webhookCache)
	}
	if bridge != nil {
		listener = listener.WithBridge(bridge)
	}
	consumerConfig := &kafka.Config{
		ClusterConfig:   clusterConfig,
		BrokerAddresses: strings.Split(settings.KafkaBrokers, ","),
		Topic:           settings.DeviceEventsTopic,
		GroupID:         "vehicle-triggers",
		MaxInFlight:     int64(settings.MaxInFlight),
		Processor:       listener.ProcessEventMessages,
		Name:            "events",
	}

	consumer, err := kafka.NewConsumer(consumerConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create device events consumer: %w", err)
	}

	return consumer, nil
}

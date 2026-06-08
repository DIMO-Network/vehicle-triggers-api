package app

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/secrets"

	"github.com/DIMO-Network/server-garage/pkg/fibercommon"
	"github.com/DIMO-Network/shared/pkg/db"
	_ "github.com/DIMO-Network/vehicle-triggers-api/docs" // Import Swagger docs
	"github.com/DIMO-Network/vehicle-triggers-api/internal/auth"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/clients/identity"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/clients/tokenexchange"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/config"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/metriclistener"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/controllers/webhook"
	vtnats "github.com/DIMO-Network/vehicle-triggers-api/internal/nats"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/auditqueue"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/cachebroadcast"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/configaudit"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggerevaluator"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggersrepo"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/triggerstate"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhookcache"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhookdispatcher"
	"github.com/DIMO-Network/vehicle-triggers-api/internal/services/webhooksender"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/redirect"
	"github.com/gofiber/swagger"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/rs/zerolog"
)

type Servers struct {
	Application *fiber.App

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
	if hexKey := strings.TrimSpace(settings.SigningSecretKeyHex); hexKey != "" {
		key, err := hex.DecodeString(hexKey)
		if err != nil {
			return nil, fmt.Errorf("SIGNING_SECRET_KEY_HEX: %w", err)
		}
		cipher, err := secrets.NewAESGCM(key)
		if err != nil {
			return nil, fmt.Errorf("AES-GCM cipher: %w", err)
		}
		repo.SetCipher(cipher)
	}

	webhookCache, err := startWebhookCache(ctx, settings, tokenExchangeCache, repo)
	if err != nil {
		return nil, fmt.Errorf("failed to start webhook cache: %w", err)
	}

	var (
		natsClient   *vtnats.Client
		natsListener *metriclistener.MetricListener
		natsSigCons  jetstream.Consumer
		natsEvtCons  jetstream.Consumer
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
				Workers: settings.Audit.Workers,
				Buffer:  settings.Audit.QueueSize,
			}, natsClient, logger)
			dispatcher = webhookdispatcher.New(webhookdispatcher.Config{
				Workers:           settings.Dispatcher.Workers,
				QueueSize:         settings.Dispatcher.QueueSize,
				MaxFailureCount:   maxFailureCount,
				RetryAttempts:     settings.Dispatcher.RetryAttempts,
				RetryInitialDelay: settings.Dispatcher.RetryInitialDelay,
				PerHostRPS:        settings.Dispatcher.PerHostRPS,
				PerHostBurst:      settings.Dispatcher.PerHostBurst,
			}, webhookSender, stateStore, auditQ, repo, logger)
			// Promote the dispatcher's per-pod limiter to a cluster-shared
			// one when RATE_LIMIT_BUCKET is configured and reachable. KV
			// hiccups degrade gracefully to the per-pod limiter inside
			// clusterLimiter.Wait; an empty bucket name skips it entirely.
			if settings.Dispatcher.PerHostRPS > 0 && settings.NATS.RateLimitBucket != "" {
				if rlKV, err := natsClient.RateLimit(ctx); err != nil {
					logger.Warn().Err(err).Msg("cluster rate limit KV unavailable; using per-pod limiter")
				} else {
					dispatcher = dispatcher.WithClusterLimiter(rlKV)
				}
			}
			natsListener = buildListener(settings, tokenExchangeCache, repo, webhookCache, stateStore, dispatcher)
			// At cutover a brand-new durable defaults to DeliverNew so the
			// service doesn't replay retained telemetry and fire stale
			// webhooks; "all" is opt-in for backfill/replay. Ignored once the
			// durable exists (JetStream fixes deliver policy at creation).
			deliverPolicy := jetstream.DeliverNewPolicy
			if settings.NATS.DeliverPolicy == "all" {
				deliverPolicy = jetstream.DeliverAllPolicy
			}
			natsSigCons, err = natsClient.EnsureConsumer(ctx, vtnats.ConsumerSpec{
				Stream:         settings.NATS.SignalsStream,
				Durable:        settings.NATS.SignalsDurable,
				FilterSubjects: []string{vtnats.AllSignalsFilter()},
				DeliverPolicy:  deliverPolicy,
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
				DeliverPolicy:  deliverPolicy,
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
	audit := configaudit.New(natsClient)
	webhookController, err := webhook.NewWebhookController(repo, webhookCache, settings.MaxAllowedCooldownPeriod)
	if err != nil {
		return nil, fmt.Errorf("failed to create webhook controller: %w", err)
	}
	webhookController.WithAudit(audit)
	vehicleSubscriptionController := webhook.NewVehicleSubscriptionController(repo, identityClient, tokenExchangeClient, webhookCache).WithAudit(audit)

	app.Get("/health", func(c *fiber.Ctx) error {
		body := fiber.Map{
			"data": "Server is up and running",
		}
		healthy := true
		// DB ping comes first. Without it a Postgres outage leaves the
		// pod in service routing webhook creates that will fail at write,
		// surfacing as 500s instead of a readiness-probe-driven drain.
		ctx, cancel := context.WithTimeout(c.UserContext(), 2*time.Second)
		defer cancel()
		if err := repo.Ping(ctx); err != nil {
			body["db"] = map[string]any{"healthy": false, "error": err.Error()}
			healthy = false
		} else {
			body["db"] = map[string]any{"healthy": true}
		}
		if natsClient != nil {
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
				healthy = false
			}
		}
		if !healthy {
			return c.Status(fiber.StatusServiceUnavailable).JSON(body)
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
	devJWTAuth.Post("/v1/webhooks/:webhookId/rotate-secret", webhookController.RotateSigningSecret)

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
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				webhookCache.ScheduleRefresh(ctx)
			}
		}
	}()

	logger.Info().Msg("webhook cache started")

	return webhookCache, nil
}

// buildListener builds a listener whose evaluator consults the supplied
// state store for cooldown / previousValue lookups and hands fires off to
// the supplied dispatcher. State and dispatcher are required for the
// production path; only the unit test harness passes nil.
func buildListener(settings *config.Settings, tokenExchangeCache *tokenexchange.Cache, repo *triggersrepo.Repository, webhookCache *webhookcache.WebhookCache, state triggerevaluator.StateStore, dispatcher metriclistener.WebhookDispatcher) *metriclistener.MetricListener {
	evaluator := triggerevaluator.NewTriggerEvaluator(tokenExchangeCache)
	if state != nil {
		evaluator = evaluator.WithStateStore(state)
	}
	return metriclistener.NewMetricsListener(webhookCache, repo, evaluator, dispatcher, settings)
}

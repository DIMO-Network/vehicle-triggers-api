# Vehicle Triggers API - Developer Guide

## Table of Contents

1. [Architecture Overview](#architecture-overview)
   - [High-Level Architecture](#high-level-architecture)
2. [Core Concepts](#core-concepts)
   - [1. Triggers (Webhooks)](#1-triggers-webhooks)
   - [2. Vehicle Subscriptions](#2-vehicle-subscriptions)
   - [3. Trigger Logs](#3-trigger-logs)
   - [4. CEL Conditions](#4-cel-conditions)
3. [System Flow](#system-flow)
   - [Flow 1: Creating a Webhook (Trigger)](#flow-1-creating-a-webhook-trigger)
   - [Flow 2: Subscribing Vehicles to a Webhook](#flow-2-subscribing-vehicles-to-a-webhook)
   - [Flow 3: Signal Processing (The Core Loop)](#flow-3-signal-processing-the-core-loop)
   - [Flow 4: Event Processing](#flow-4-event-processing)
4. [Key Components](#key-components)
   - [1. Webhook Cache](#1-webhook-cache-internalserviceswebhookcache)
   - [2. Trigger Evaluator](#2-trigger-evaluator-internalservicestriggerevaluator)
   - [3. Triggers Repository](#3-triggers-repository-internalservicestriggersrepo)
   - [4. Webhook Sender](#4-webhook-sender-internalserviceswebhooksender)
   - [5. CEL Condition Engine](#5-cel-condition-engine-internalcelcondition)
   - [6. Metric Listener](#6-metric-listener-internalcontrollersmetriclistener)
   - [7. Signal Definitions](#7-signal-definitions-internalsignals)
5. [Database Schema](#database-schema)
   - [Tables](#tables)
     - [`triggers`](#triggers)
     - [`vehicle_subscriptions`](#vehicle_subscriptions)
     - [`trigger_logs`](#trigger_logs)
6. [Common Development Tasks](#common-development-tasks)
   - [Adding a New CEL Variable](#adding-a-new-cel-variable)
   - [Adding a New Signal Type](#adding-a-new-signal-type)
   - [Changing Webhook Failure Behavior](#changing-webhook-failure-behavior)
7. [Troubleshooting](#troubleshooting)
   - [Problem: Webhook Not Firing](#problem-webhook-not-firing)
   - [Problem: Webhook Repeatedly Failing](#problem-webhook-repeatedly-failing)
   - [Problem: Cache Not Updating](#problem-cache-not-updating)
   - [Problem: CEL Condition Validation Errors](#problem-cel-condition-validation-errors)
   - [Problem: Permission Denied Errors](#problem-permission-denied-errors)
   - [Problem: Database Migration Issues](#problem-database-migration-issues)
8. [Testing](#testing)
   - [Running Tests](#running-tests)
   - [Mock Generation](#mock-generation)
9. [Configuration](#configuration)
   - [Environment Variables](#environment-variables)
10. [Deployment](#deployment)
    - [Building](#building)
    - [Docker](#docker)
    - [Helm Chart](#helm-chart)
11. [Additional Resources](#additional-resources)
12. [Future Improvements](#future-improvements)
    - [1. Webhook Cache Optimization](#1-webhook-cache-optimization)
    - [2. Event-Based Webhooks with Tag Filtering](#2-event-based-webhooks-with-tag-filtering)
    - [3. Permission Caching](#3-permission-caching)

---

## Architecture Overview

The Vehicle Triggers API is a webhook-based notification system that monitors vehicle telemetry data (signals and events) from Kafka topics and triggers HTTP webhooks when user-defined conditions are met.

### High-Level Architecture

```
┌─────────────┐
│   Kafka     │ Signals Topic (vehicle telemetry)
│   Topics    │ Events Topic (vehicle events)
└──────┬──────┘
       │
       ↓
┌─────────────────────────────────────────────────┐
│        Metric Listener (Kafka Consumer)         │
│  • Consumes signals/events                      │
│  • Looks up webhooks from cache                 │
│  • Evaluates CEL conditions                     │
│  • Sends HTTP webhooks                          │
└─────────────────────────────────────────────────┘
       ↑                                    ↓
       │                              ┌──────────┐
┌──────┴──────┐                       │ Webhook  │
│  Webhook    │                       │ Target   │
│  Cache      │                       │ URLs     │
│ (In-Memory) │                       └──────────┘
└──────┬──────┘
       │
       ↓
┌─────────────────────────────────────────────────┐
│              REST API (Fiber)                   │
│  • CRUD operations for webhooks                 │
│  • Vehicle subscription management              │
│  • JWT authentication                           │
└─────────────────────────────────────────────────┘
       ↓
┌─────────────┐
│  PostgreSQL │ (Triggers, Subscriptions, Logs)
└─────────────┘
```

**Key Files:**

- Application entry point: [`cmd/vehicle-triggers-api/main.go`](cmd/vehicle-triggers-api/main.go)
- App initialization: [`internal/app/app.go`](internal/app/app.go)

---

## Core Concepts

### 1. Triggers (Webhooks)

A **trigger** (also called a webhook) is a user-defined rule that monitors vehicle telemetry data and fires HTTP requests when conditions are met.

**Database Table:** `triggers`  
**Model:** [`internal/db/models/triggers.go`](internal/db/models/triggers.go)

**Key Fields:**

- `id`: UUID of the webhook
- `service`: Either `telemetry.signals` or `telemetry.events`
- `metric_name`: The signal/event name to monitor (e.g., "speed", "HarshBraking")
- `condition`: CEL expression that evaluates to true/false
- `target_uri`: HTTPS endpoint to POST webhooks to
- `cooldown_period`: Minimum seconds between successive webhook calls
- `developer_license_address`: Ethereum address of the developer who owns this webhook
- `display_name`: User-friendly name for the webhook
- `status`: `enabled`, `disabled`, `failed`, or `deleted`
- `failure_count`: Number of consecutive failures (auto-disabled at threshold)

**Code References:**

- Repository: [`internal/services/triggersrepo/triggersrepo.go`](internal/services/triggersrepo/triggersrepo.go)
- Controller: [`internal/controllers/webhook/webhook_controller.go`](internal/controllers/webhook/webhook_controller.go)

### 2. Vehicle Subscriptions

A **subscription** is a many-to-many relationship between vehicles and webhooks. A webhook only fires for vehicles it's subscribed to.

**Database Table:** `vehicle_subscriptions`  
**Model:** [`internal/db/models/vehicle_subscriptions.go`](internal/db/models/vehicle_subscriptions.go)

**Key Fields:**

- `asset_did`: Vehicle DID (e.g., `did:erc721:137:0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF:12345`)
- `trigger_id`: UUID of the webhook

**Why Subscriptions Exist:**

- Allows developers to target specific vehicles
- Enables permission checking (developer must have access to the vehicle)
- Supports both "subscribe all" and "subscribe list" patterns

**Code References:**

- Controller: [`internal/controllers/webhook/vehicle_subscription_controller.go`](internal/controllers/webhook/vehicle_subscription_controller.go)
- Repository: [`internal/services/triggersrepo/triggersrepo.go`](internal/services/triggersrepo/triggersrepo.go) (lines 376-539)

### 3. Trigger Logs

Logs of when webhooks were successfully triggered, including snapshot data for cooldown and previous value comparisons.

**Database Table:** `trigger_logs`  
**Model:** [`internal/db/models/trigger_logs.go`](internal/db/models/trigger_logs.go)

**Purpose:**

- Track `last_triggered_at` for cooldown enforcement
- Store `snapshot_data` (previous signal/event JSON) for CEL conditions using `previousValue`
- Audit trail of webhook deliveries

### 4. CEL Conditions

[CEL (Common Expression Language)](https://github.com/google/cel-spec) is used for flexible condition evaluation.

**Signal Variables Available:**

```javascript
value; // Generic value (number, string, or location)
valueNumber; // Numeric value
valueString; // String value
source; // Signal source identifier
previousValueNumber; // Previous numeric value
previousValueString; // Previous string value
previousSource; // Previous source

// For location signals:
value.Latitude;
value.Longitude;
value.HDOP;
previousValue.Latitude;
previousValue.Longitude;
previousValue.HDOP;
```

**Event Variables Available:**

```javascript
name; // Event name
source; // Event source
durationNs; // Event duration in nanoseconds
metadata; // Event metadata
previousName; // Previous event name
previousSource; // Previous event source
previousDurationNs; // Previous duration
previousMetadata; // Previous metadata
```

**Code References:**

- CEL engine: [`internal/celcondition/celcondition.go`](internal/celcondition/celcondition.go)
- Signal definitions: [`internal/signals/signals.go`](internal/signals/signals.go)

---

## System Flow

### Flow 1: Creating a Webhook (Trigger)

```
1. Developer makes POST /v1/webhooks
   ↓
2. Webhook Controller validates request
   • Target URL validation
   • CEL condition validation
   • Cooldown period validation
   ↓
3. Controller verifies target URL
   • Sends {"verification": "test"} POST
   • Expects 200 + verification token back
   ↓
4. Repository creates trigger in database
   • Assigns UUID
   • Sets display_name (or uses ID if not provided)
   ↓
5. Returns webhook ID to developer
```

**Code Path:**

1. Endpoint: [`internal/controllers/webhook/webhook_controller.go`](internal/controllers/webhook/webhook_controller.go) (lines 65-122, `RegisterWebhook`)
2. Validation: [`internal/controllers/webhook/validation.go`](internal/controllers/webhook/validation.go)
3. Repository: [`internal/services/triggersrepo/triggersrepo.go`](internal/services/triggersrepo/triggersrepo.go) (lines 89-136, `CreateTrigger`)

### Flow 2: Subscribing Vehicles to a Webhook

```
1. Developer makes POST /v1/webhooks/{webhookId}/subscribe/{assetDID}
   (or uses /subscribe/list or /subscribe/all)
   ↓
2. Vehicle Subscription Controller validates ownership
   • Checks if webhook belongs to developer
   ↓
3. Controller checks permissions via Token Exchange API
   • Validates developer has required privileges for vehicle
   • Signal-specific permissions (e.g., GetLocationHistory)
   ↓
4. Repository creates subscription record
   ↓
5. Cache refresh is scheduled
   • Debounced refresh (default 5 seconds)
   • Rebuilds in-memory webhook cache
```

**Code Path:**

1. Endpoint: [`internal/controllers/webhook/vehicle_subscription_controller.go`](internal/controllers/webhook/vehicle_subscription_controller.go) (lines 68-117, `AssignVehicleToWebhook`)
2. Permission check: Lines 95-108
3. Repository: [`internal/services/triggersrepo/triggersrepo.go`](internal/services/triggersrepo/triggersrepo.go) (lines 378-429, `CreateVehicleSubscription`)
4. Cache: [`internal/services/webhookcache/webhook_cache.go`](internal/services/webhookcache/webhook_cache.go) (lines 71-85, `ScheduleRefresh`)

### Flow 3: Signal Processing (The Core Loop)

```
┌─────────────────────────────────────────────────┐
│ 1. Kafka message arrives                        │
│    Topic: topics.signals                        │
└───────────────┬─────────────────────────────────┘
                ↓
┌─────────────────────────────────────────────────┐
│ 2. MetricListener.processSignalMessage          │
│    • Parse signal JSON                          │
│    • Decode vehicle DID                         │
│    • Get signal definition                      │
└───────────────┬─────────────────────────────────┘
                ↓
┌─────────────────────────────────────────────────┐
│ 3. Query webhook cache                          │
│    webhookCache.GetWebhooks(vehicleDID,         │
│                              "telemetry.signals",│
│                              signalName)         │
└───────────────┬─────────────────────────────────┘
                ↓
         ┌──────┴──────┐
         │ For each    │
         │ webhook     │
         └──────┬──────┘
                ↓
┌─────────────────────────────────────────────────┐
│ 4. TriggerEvaluator.EvaluateSignalTrigger       │
│    ├─ Check permissions (Token Exchange)        │
│    │  • If denied → unsubscribe vehicle         │
│    ├─ Check cooldown period                     │
│    │  • Compare against last_triggered_at       │
│    └─ Evaluate CEL condition                    │
│       • Get previous value from trigger_logs    │
│       • Evaluate with current & previous data   │
└───────────────┬─────────────────────────────────┘
                ↓
         ┌──────┴──────────┐
         │ Condition met?  │
         └──────┬──────────┘
                ↓ Yes
┌─────────────────────────────────────────────────┐
│ 5. WebhookSender.SendWebhook                    │
│    • Create CloudEvent payload                  │
│    • POST to target_uri                         │
│    • Handle success/failure                     │
│      ├─ Success: Reset failure_count            │
│      └─ Failure: Increment failure_count        │
│                  (disable if >= max threshold)  │
└───────────────┬─────────────────────────────────┘
                ↓
┌─────────────────────────────────────────────────┐
│ 6. Create trigger log                           │
│    • Save snapshot_data (current signal JSON)   │
│    • Record last_triggered_at timestamp         │
└─────────────────────────────────────────────────┘
```

**Code Path:**

1. Kafka consumer: [`internal/controllers/metriclistener/signal.go`](internal/controllers/metriclistener/signal.go) (lines 21-57, `processSignalMessage`)
2. Cache lookup: [`internal/services/webhookcache/webhook_cache.go`](internal/services/webhookcache/webhook_cache.go) (lines 87-100, `GetWebhooks`)
3. Evaluation: [`internal/services/triggerevaluator/trigger_evaluator.go`](internal/services/triggerevaluator/trigger_evaluator.go) (lines 63-135, `EvaluateSignalTrigger`)
4. Webhook sending: [`internal/services/webhooksender/webhook_sender.go`](internal/services/webhooksender/webhook_sender.go) (lines 50-97, `SendWebhook`)
5. Logging: [`internal/controllers/metriclistener/metric_listener.go`](internal/controllers/metriclistener/metric_listener.go) (lines 151-165, `logWebhookTrigger`)

### Flow 4: Event Processing

Events follow a similar flow to signals but:

- Come from a different Kafka topic (`DEVICE_EVENTS_TOPIC`)
- Use different CEL variables (name, source, durationNs, metadata)
- Have different payload structure

**Code Path:**

- Event processor: [`internal/controllers/metriclistener/events.go`](internal/controllers/metriclistener/events.go) (lines 20-101)

---

## Key Components

### 1. Webhook Cache (`internal/services/webhookcache/`)

**Purpose:** In-memory cache of active webhooks indexed by vehicle DID and signal/event name for fast lookup.

**Structure:**

```go
map[assetDID]map["service:metricName"][]*Webhook
```

Example:

```go
{
  "did:erc721:137:0xbA...cF:12345": {
    "telemetry.signals:speed": [webhook1, webhook2],
    "telemetry.events:HarshBraking": [webhook3]
  }
}
```

**Key Methods:**

- `PopulateCache()`: Loads all subscriptions and triggers from database
- `GetWebhooks(assetDID, service, metricName)`: Fast lookup for signal processing
- `ScheduleRefresh()`: Debounced cache refresh after CRUD operations

**When to Update:**

- **Problem:** Webhook not firing for a vehicle
- **Solution:** Check if subscription exists and cache has been refreshed
- **File:** [`internal/services/webhookcache/webhook_cache.go`](internal/services/webhookcache/webhook_cache.go)

**Cache Refresh Triggers:**

- Every 1 minute (automatic background refresh)
- After webhook CRUD operations (debounced by 5 seconds)
- After subscription changes

### 2. Trigger Evaluator (`internal/services/triggerevaluator/`)

**Purpose:** Encapsulates the logic for evaluating whether a webhook should fire.

**Evaluation Steps:**

1. **Permission Check:** Calls Token Exchange API to verify developer has access
2. **Cooldown Check:** Ensures enough time has passed since last trigger
3. **Condition Evaluation:** Runs CEL program with current and previous data

**Return Values:**

```go
type TriggerEvaluationResult struct {
    ShouldFire       bool  // Should webhook be sent?
    CoolDownNotMet   bool  // Cooldown period not elapsed
    PermissionDenied bool  // Developer lacks permission
    ConditionNotMet  bool  // CEL condition evaluated to false
}
```

**When to Update:**

- **Problem:** Changing evaluation logic or adding new condition types
- **File:** [`internal/services/triggerevaluator/trigger_evaluator.go`](internal/services/triggerevaluator/trigger_evaluator.go)

### 3. Triggers Repository (`internal/services/triggersrepo/`)

**Purpose:** Database abstraction layer for all trigger and subscription operations.

**Key Methods:**

- `CreateTrigger()`: Inserts new webhook with validation
- `GetTriggersByDeveloperLicense()`: Lists webhooks for a developer
- `CreateVehicleSubscription()`: Links vehicle to webhook
- `GetLastLogValue()`: Retrieves last trigger log (for cooldown & previous value)
- `IncrementTriggerFailureCount()`: Handles webhook delivery failures
- `ResetTriggerFailureCount()`: Resets count on successful delivery

**When to Update:**

- **Problem:** Adding new database queries or changing data access patterns
- **File:** [`internal/services/triggersrepo/triggersrepo.go`](internal/services/triggersrepo/triggersrepo.go)

### 4. Webhook Sender (`internal/services/webhooksender/`)

**Purpose:** Handles HTTP delivery of webhook payloads.

**Features:**

- 30-second timeout for webhook requests
- CloudEvent format payloads
- Failure detection (4xx/5xx status codes)
- Error logging with response body (limited to 1KB)

**When to Update:**

- **Problem:** Webhook delivery reliability issues or timeout adjustments
- **File:** [`internal/services/webhooksender/webhook_sender.go`](internal/services/webhooksender/webhook_sender.go)

### 5. CEL Condition Engine (`internal/celcondition/`)

**Purpose:** Compiles and evaluates CEL expressions for triggers.

**Key Functions:**

- `PrepareSignalCondition()`: Compiles CEL for signals with validation
- `EvaluateSignalCondition()`: Evaluates signal condition with current/previous data
- `PrepareEventCondition()`: Compiles CEL for events
- `EvaluateEventCondition()`: Evaluates event condition

**Custom Functions:**

- `geoDistance(lat1, lon1, lat2, lon2)`: Returns distance in kilometers using Haversine formula

**When to Update:**

- **Problem:** Adding new CEL variables, functions, or changing condition syntax
- **File:** [`internal/celcondition/celcondition.go`](internal/celcondition/celcondition.go)

### 6. Metric Listener (`internal/controllers/metriclistener/`)

**Purpose:** Kafka consumer that orchestrates the entire trigger evaluation and webhook delivery process.

**Key Methods:**

- `ProcessSignalMessages()`: Main loop for signal processing
- `ProcessEventMessages()`: Main loop for event processing
- `processSignalWebhook()`: Handles single webhook evaluation and delivery
- `ShouldAttemptWebhook()`: Circuit breaker logic (checks status & failure count)
- `handleTriggeredWebhook()`: Sends webhook and handles success/failure

**When to Update:**

- **Problem:** Changing overall signal/event processing flow
- **Files:**
  - [`internal/controllers/metriclistener/metric_listener.go`](internal/controllers/metriclistener/metric_listener.go)
  - [`internal/controllers/metriclistener/signal.go`](internal/controllers/metriclistener/signal.go)
  - [`internal/controllers/metriclistener/events.go`](internal/controllers/metriclistener/events.go)

### 7. Signal Definitions (`internal/signals/`)

**Purpose:** Loads vehicle signal metadata from model-garage schema.

**Data Provided:**

- Signal name (e.g., "speed")
- Value type (float64, string, vss.Location)
- Unit (e.g., "km/h")
- Description
- Required permissions

**When to Update:**

- **Problem:** Adding new signal types or changing permission requirements
- **File:** [`internal/signals/signals.go`](internal/signals/signals.go)

---

## Database Schema

### Tables

#### `triggers`

```sql
id                       uuid PRIMARY KEY
service                  text NOT NULL  -- "telemetry.signals" or "telemetry.events"
metric_name              text NOT NULL  -- Signal/event name
condition                text NOT NULL  -- CEL expression
target_uri               text NOT NULL  -- Webhook URL
cooldown_period          integer NOT NULL DEFAULT 0
developer_license_address bytea NOT NULL  -- Ethereum address
display_name             text NOT NULL  -- User-friendly name (unique per developer)
status                   text NOT NULL  -- 'enabled', 'disabled', 'failed', 'deleted'
description              text
failure_count            integer NOT NULL DEFAULT 0
created_at               timestamptz NOT NULL
updated_at               timestamptz NOT NULL
```

**Indexes:**

- Unique index on `(developer_license_address, display_name)` where `status != 'deleted'`

#### `vehicle_subscriptions`

```sql
asset_did    text NOT NULL     -- Vehicle DID (ERC721)
trigger_id   uuid NOT NULL     -- References triggers(id)
created_at   timestamptz NOT NULL
updated_at   timestamptz NOT NULL

PRIMARY KEY (asset_did, trigger_id)
FOREIGN KEY (trigger_id) REFERENCES triggers(id)
```

#### `trigger_logs`

```sql
id                uuid PRIMARY KEY
asset_did         text NOT NULL     -- Vehicle DID
trigger_id        uuid NOT NULL     -- References triggers(id)
snapshot_data     jsonb NOT NULL    -- Previous signal/event JSON
last_triggered_at timestamptz NOT NULL
created_at        timestamptz NOT NULL
failure_reason    text

FOREIGN KEY (trigger_id) REFERENCES triggers(id)
```

**Migration Files:**

- Initial schema: [`internal/db/migrations/00001_init.sql`](internal/db/migrations/00001_init.sql)
- Asset DID migration: [`internal/db/migrations/00002_asset_did.sql`](internal/db/migrations/00002_asset_did.sql)

---

## Common Development Tasks

### Adding a New CEL Variable

1. **Update CEL Environment** in [`internal/celcondition/celcondition.go`](internal/celcondition/celcondition.go)

   ```go
   // For signals (PrepareSignalCondition):
   cel.Variable("newVariable", cel.StringType),

   // Don't forget to add to validation vars:
   vars := map[string]any{
       "newVariable": "default_value",
   }
   ```

2. **Update Evaluation** in `EvaluateSignalCondition()`:

   ```go
   vars := map[string]any{
       "newVariable": signal.NewField,
       // ...
   }
   ```

3. **Update Documentation** in README.md

### Adding a New Signal Type

1. **Update Signal Loader** in [`internal/signals/signals.go`](internal/signals/signals.go)

   - Signals are automatically loaded from `model-garage` schema
   - If adding a custom signal type, define the value type constant

2. **Update CEL Condition Preparation** in [`internal/celcondition/celcondition.go`](internal/celcondition/celcondition.go)

   - Add new value type case in `PrepareSignalCondition()`
   - Define CEL variables for the new type

3. **Update Payload Creation** in [`internal/controllers/metriclistener/signal.go`](internal/controllers/metriclistener/signal.go)
   - Add case in `createSignalPayload()` to handle new type

### Changing Webhook Failure Behavior

The failure handling logic implements a circuit breaker pattern:

1. **Configuration** in [`internal/config/settings.go`](internal/config/settings.go):

   ```go
   MaxWebhookFailureCount uint `env:"MAX_WEBHOOK_FAILURE_COUNT"`
   ```

2. **Circuit Breaker Check** in [`internal/controllers/metriclistener/metric_listener.go`](internal/controllers/metriclistener/metric_listener.go) (lines 192-205):

   ```go
   func (m *MetricListener) ShouldAttemptWebhook(trigger *models.Trigger) bool {
       if trigger.Status != triggersrepo.StatusEnabled {
           return false
       }
       if trigger.FailureCount >= m.maxFailureCount {
           return false
       }
       return true
   }
   ```

3. **Failure Increment** in [`internal/services/triggersrepo/triggersrepo.go`](internal/services/triggersrepo/triggersrepo.go) (lines 677-711):

   - Status changes to `failed` when threshold reached
   - Uses row-level locking to prevent race conditions

4. **Reset on Success** in [`internal/services/triggersrepo/triggersrepo.go`](internal/services/triggersrepo/triggersrepo.go) (lines 640-675):
   - Resets `failure_count` to 0
   - Re-enables webhook if it was in `failed` status

---

## Troubleshooting

### Problem: Webhook Not Firing

**Checklist:**

1. ✅ Is the webhook enabled? Check `status` in `triggers` table

   ```sql
   SELECT id, status, failure_count FROM triggers WHERE id = 'webhook-uuid';
   ```

2. ✅ Is the vehicle subscribed? Check `vehicle_subscriptions` table

   ```sql
   SELECT * FROM vehicle_subscriptions
   WHERE trigger_id = 'webhook-uuid' AND asset_did = 'vehicle-did';
   ```

3. ✅ Is the webhook in the cache? Check cache refresh logs

   - Look for "failed to populate webhook cache" errors
   - Cache refreshes every 1 minute automatically

4. ✅ Does the CEL condition evaluate to true? Test in logs

   ```sql
   SELECT condition FROM triggers WHERE id = 'webhook-uuid';
   ```

5. ✅ Is cooldown period preventing trigger? Check last trigger time

   ```sql
   SELECT last_triggered_at, cooldown_period
   FROM trigger_logs tl
   JOIN triggers t ON t.id = tl.trigger_id
   WHERE tl.trigger_id = 'webhook-uuid'
   ORDER BY last_triggered_at DESC LIMIT 1;
   ```

6. ✅ Does developer have permission? Check Token Exchange API
   - Look for "Insufficient vehicle permissions" in logs
   - Verify signal permissions match requirements

**Code References:**

- Permission check: [`internal/services/triggerevaluator/trigger_evaluator.go`](internal/services/triggerevaluator/trigger_evaluator.go) (lines 65-79)
- Cooldown check: [`internal/services/triggerevaluator/trigger_evaluator.go`](internal/services/triggerevaluator/trigger_evaluator.go) (lines 216-222)
- Condition evaluation: [`internal/celcondition/celcondition.go`](internal/celcondition/celcondition.go) (lines 225-263)

### Problem: Webhook Repeatedly Failing

**Symptoms:**

- `failure_count` increasing in `triggers` table
- Status changes to `failed` after threshold

**Investigation:**

1. Check webhook sender logs for HTTP errors
2. Verify target URL is reachable
3. Check response status code in logs
4. Look at webhook response body (logged on failure)

**Fix:**

1. Update webhook URL: `PUT /v1/webhooks/{webhookId}`
2. Failure count auto-resets to 0 on successful delivery
3. Status changes back to `enabled` on success

**Code References:**

- Failure handling: [`internal/controllers/metriclistener/metric_listener.go`](internal/controllers/metriclistener/metric_listener.go) (lines 117-149)
- Increment counter: [`internal/services/triggersrepo/triggersrepo.go`](internal/services/triggersrepo/triggersrepo.go) (lines 677-711)

### Problem: Cache Not Updating

**Symptoms:**

- New subscriptions not working immediately
- Updated webhooks still using old conditions

**Investigation:**

1. Check `ScheduleRefresh()` is being called after CRUD operations
2. Verify debounce logic isn't preventing refresh
3. Look for errors in `PopulateCache()` logs

**Quick Fix:**

- Restart the service (cache rebuilds on startup)
- Wait for automatic 1-minute refresh

**Code References:**

- Cache refresh: [`internal/services/webhookcache/webhook_cache.go`](internal/services/webhookcache/webhook_cache.go) (lines 56-69 and 71-85)
- Background refresh: [`internal/app/app.go`](internal/app/app.go) (lines 156-163)

### Problem: CEL Condition Validation Errors

**Common Mistakes:**

1. Using wrong variable names (e.g., `val` instead of `value`)
2. Type mismatches (comparing string to number)
3. Forgetting cross-type comparison support

**Testing Conditions:**

```bash
# Test condition during webhook creation
curl -X POST http://localhost:8080/v1/webhooks \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "service": "telemetry.signals",
    "metricName": "speed",
    "condition": "value > 55",
    ...
  }'
```

**Code References:**

- Validation: [`internal/controllers/webhook/validation.go`](internal/controllers/webhook/validation.go)
- CEL preparation: [`internal/celcondition/celcondition.go`](internal/celcondition/celcondition.go) (lines 153-223)

### Problem: Permission Denied Errors

**Symptoms:**

- Subscriptions auto-deleted
- "Insufficient vehicle permissions" in logs

**Causes:**

1. Developer license doesn't have required privileges
2. Signal requires location permission but developer only has non-location
3. Vehicle access revoked after subscription created

**Fix:**

- Check signal permissions: `GET /v1/webhooks/signals`
- Verify developer has correct privileges in Token Exchange API
- Signal definitions: [`internal/signals/signals.go`](internal/signals/signals.go)

**Code References:**

- Permission check: [`internal/services/triggerevaluator/trigger_evaluator.go`](internal/services/triggerevaluator/trigger_evaluator.go) (lines 65-79 for signals)
- Auto-unsubscribe: [`internal/controllers/metriclistener/signal.go`](internal/controllers/metriclistener/signal.go) (lines 66-75)

### Problem: Database Migration Issues

**Running Migrations:**

```bash
# Run migrations only
go run cmd/vehicle-triggers-api/main.go -migrate-only

# Run specific migration command
go run cmd/vehicle-triggers-api/main.go -migrations "up"
```

**Creating New Migration:**

```bash
make add-migration name=add_new_feature
```

**Code References:**

- Migration runner: [`internal/db/migrations/migrations.go`](internal/db/migrations/migrations.go)
- Migration files: [`internal/db/migrations/`](internal/db/migrations/)

---

## Testing

### Running Tests

```bash
# Run all tests
make test
```

### Mock Generation

Mocks are used for unit testing. To regenerate:

```bash
make generate
```

---

## Configuration

### Environment Variables

The application reads environment variables from a local `.env` file in the project root. To get started:

```bash
cp sample.env .env
```

Then edit the `.env` file with your local configuration values. The `.env` file is gitignored, so your local settings won't be committed to the repository.

All configuration variables are loaded and validated in [`internal/config/settings.go`](internal/config/settings.go).

---

## Deployment

### Building

```bash
make build
```

### Docker

```bash
make docker
```

### Helm Chart

Kubernetes deployment configuration is in [`charts/vehicle-triggers-api/`](charts/vehicle-triggers-api/)

---

## Additional Resources

- **Swagger Documentation**: `GET /swagger` (when running locally)
- **CloudEvent Spec**: https://github.com/DIMO-Network/cloudevent
- **CEL Spec**: https://github.com/google/cel-spec
- **Model Garage** (signal definitions): https://github.com/DIMO-Network/model-garage

---

## Future Improvements

### 1. Webhook Cache Optimization

**Current Issue**: When the webhook cache updates, it has to re-create all webhooks in memory. As the number of webhooks grows, this repopulation process could become a performance bottleneck.

**Proposed Solutions**:

- **Versioning Approach**: Add versioning to webhooks and only re-populate webhooks when their version changes. This would allow selective updates rather than full cache rebuilds.
- **Distributed Cache**: Migrate the webhook cache to a distributed cache system like Redis. This would provide:
  - Shared cache across multiple service instances
  - Persistence and faster recovery on restarts
  - Built-in TTL and eviction policies
  - Reduced memory pressure on application instances

### 2. Event-Based Webhooks with Tag Filtering

**Current Limitation**: Event-triggered webhooks can only be triggered by exact event names, which limits flexibility and requires separate webhooks for semantically similar events.

**Proposed Enhancement**: Allow webhooks to be triggered by event tags instead of (or in addition to) event names. This would enable:

- Grouping related events under common tags (e.g., `harsh_breaking`, `safety_critical`, `maintenance_required`)
- A single webhook to handle multiple related event types
- More maintainable and flexible event routing

**Example**: A webhook configured with the tag `harsh_breaking` would trigger for any event tagged with that category, regardless of the specific event name.

### 3. Permission Caching

**Current Issue**: Permissions are checked on every webhook evaluation, which can be slow and resource-intensive, especially under high load.

**Proposed Solution**: Implement a permission caching layer with a time-to-live (TTL) mechanism:

- Cache permission check results for 15 minutes (similar to JWT expiration patterns)
- Use the user/vehicle combination as the cache key
- Significantly reduce latency for webhook evaluations
- Balance security requirements with performance needs

### 4. Credit Tracking
- tracking and or rate limiting per dev 

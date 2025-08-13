# vehicle-triggers-api

## Overview

The DIMO Vehicle Triggers API is being developed to deliver real-time vehicle notifications via webhooks. It aims to provide scalable and flexible event handling using Kafka.

## How To

Most actions are done via make commands. use `make help` to see all available commands.

```bash
make help
Specify a subcommand:
  build                build the binary
  run                  run the binary
  clean                clean the binary
  install              install the binary
  tidy                 tidy the go mod
  test                 run tests
  lint                 run linter
  docker               build docker image
  tools-golangci-lint  install golangci-lint
  add-migration        Generate migration file specify name with name=your_migration_name
  generate             run all file generation for the project
  generate-swagger     generate swagger documentation
  generate-go          run go generate
  ...
```

To create a db:

`$ docker compose up -d`

Add a migrations:
`$ make add-migration name=<migration_name>`

Migrate DB to latest:
`$ go run cmd/vehicle-triggers-api/main.go -migrate="only"`

To regenerate models:
`$ make generate-sqlboiler`

# Running locally

- in your postgres: `create database vehicle_triggers_api with owner dimo;` make sure you have user `dimo` setup in your local db.
- check to see if kafka is installed locally `brew services list`
- install kafka with brew services
- list all topics: `/opt/homebrew/opt/kafka/bin/kafka-topics --list --bootstrap-server localhost:9092`
- Create a topic:
- `/opt/homebrew/opt/kafka/bin/kafka-topics --create --topic topics.signals --bootstrap-server localhost:9092 --partitions 1 --replication-factor 1`
- Make sure to check .env to have the correct KAFKA_BROKERS and DEVICE_SIGNALS_TOPIC name

## API Documentation

The Vehicle Triggers API provides webhook functionality for real-time vehicle telemetry notifications. Webhooks can be triggered by vehicle signals (like speed) or events (like harsh braking).

### Webhook Registration

To register a webhook, make a POST request to `/v1/webhooks` with the following payload:

```json
{
  "service": "telemetry.signals",
  "metricName": "speed",
  "condition": "value > 55",
  "coolDownPeriod": 30,
  "description": "Alert when vehicle speed exceeds 55 mph",
  "displayName": "Speed Alert",
  "targetURL": "https://example.com/webhook",
  "status": "enabled",
  "verificationToken": "your-verification-token"
}
```

#### Required Fields

- `service`: The subsystem producing the metric. Must be either "telemetry.signals" or "telemetry.events"
- `metricName`: The fully qualified signal/event name to monitor (e.g., "speed", "HarshBraking")
- `condition`: A CEL expression that determines when the webhook fires
- `coolDownPeriod`: Minimum seconds between successive webhook calls
- `targetURL`: HTTPS endpoint that will receive webhook notifications
- `verificationToken`: Token your endpoint must return during verification

#### Optional Fields

- `description`: Human-friendly explanation of the webhook's purpose
- `displayName`: User-friendly name for the webhook (must be unique per developer) if not provided, it will be set the to the Id of the webhook.
- `status`: Initial webhook state ("enabled" or "disabled", defaults to enabled)

### CEL Conditions

CEL (Common Expression Language) conditions determine when webhooks fire. The API validates conditions during webhook creation and provides different variables based on the service type.

#### Signal Conditions (telemetry.signals)

For signal webhooks, these variables are available in CEL expressions:

- `value`: Generic value field will either be a number or a string depending on the signal definition
- `source`: Signal source identifier
- `previousValue`: Previous signal value as a number or a string depending on the signal definition
- `previousSource`: Previous signal source

**Examples:**

```javascript
// Simple threshold
"value > 55";

// Range check
"value >= 10.0 && value <= 50.0";

// Change detection
"valueNumber != previousValueNumber";

// Combine conditions
"valueNumber > 20 && valueNumber != previousValue";

// String conditions
"valueString == 'active'";

// String contains check
"valueString.contains('emergency')";
```

#### Event Conditions (telemetry.events)

For event webhooks, these variables are available:

- `name`: Event name
- `source`: Event source identifier
- `durationNs`: Event duration in nanoseconds
- `metadata`: Event metadata as string
- `previousName`: Previous event name
- `previousSource`: Previous event source
- `previousDurationNs`: Previous event duration
- `previousMetadata`: Previous event metadata

**Examples:**

```javascript
// Specific event type
"name == 'HarshBraking'";

// Duration threshold
"durationNs > 1000000000";

// Metadata contains
"metadata.contains('emergency')";

// Complex conditions
"name == 'HarshBraking' && source == '0x1234567890abcdef1234567890abcdef12345678' && durationNs > 500";
```

#### CEL Expression Guidelines

1. **Return Boolean**: All conditions must evaluate to true/false
2. **Validation**: Conditions are validated when creating/updating webhooks
3. **Performance**: Simple conditions perform better than complex ones
4. **Cross-Type Comparisons**: Numeric comparisons work across int/float types

### Display Name Behavior

Display names provide user-friendly identification for webhooks and have specific behavior:

#### Uniqueness

- Display names must be unique per developer license
- The uniqueness constraint is case-insensitive

#### Automatic Assignment

- If no display name is provided, the webhook ID is used as the display name
- This ensures every webhook has a display name

### Available Signals

To get a list of available signals for the `metricName` field, make a GET request to `/v1/webhooks/signals`. This returns signal definitions including:

- Signal name (for use in metricName)
- Description of what the signal represents
- Unit of measurement
- Data type (determines which CEL variables to use)

### Webhook Verification

Before accepting a webhook registration, the API verifies your endpoint:

1. Sends a POST request to your targetURL with `{"verification": "test"}`
2. Expects a 200 response containing your verification token
3. Registration fails if verification doesn't succeed within 10 seconds

### Webhook Payload

When a webhook is triggered, a [CloudEvent](github.com/DIMO-Network/cloudevent?tab=readme-ov-file#example-cloudevent-json) is sent to the targetURL.
The subject of the vehicle is the [DID](https://github.com/DIMO-Network/cloudevent?tab=readme-ov-file#decentralized-identifier-did-formats) of the vehicle that triggered the webhook.

Example of a signal webhook payload:

```json
{
  "id": "16392596-22da-4599-a865-b176948069fb", // UUID of the specific payload of the webhook
  "source": "vehicle-triggers-api",
  "producer": "1fab16e0-3a51-4118-bc3a-6b6d2fecfe13", // UUID of the webhook
  "specversion": "1.0", // Version of the spec always 1.0
  "subject": "did:erc721:137:0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF:12345", // DID of the vehicle
  "time": "2025-08-13T10:15:07.630545Z", // Timestamp of the cloudevent was created
  "type": "dimo.trigger", // Type of the cloudevent always dimo.trigger when sent from a webhook
  "datacontenttype": "application/json", // Content type of the data always application/json
  "dataversion": "telemetry.signals/v1.0", // Versioning for the data field in the payload.
  "data": {
    "service": "telemetry.signals", // Service that sent the signal
    "metricName": "speed", // Name of the signal/event
    "webhookId": "1fab16e0-3a51-4118-bc3a-6b6d2fecfe13", // UUID of the webhook
    "webhookName": "Speed Alert", // Display    Name of the webhook
    "assetDID": "did:erc721:137:0xbA5738a18d83D41847dfFbDC6101d37C69c9B0cF:12345", // DID of the vehicle
    "condition": "valueNumber \u003e 20", // Condition that was used to trigger the webhook escaped for json
    "signal": {
      "name": "speed", // Name of the signal
      "unit": "km/h", // Unit of the signal
      "timestamp": "2025-08-13T10:15:04.610342Z", // Timestamp of the signal was captured
      "source": "0xF26421509Efe92861a587482100c6d728aBf1CD0", // 0xAddress of the connection that produced the signal
      "producer": "did:erc721:137:0x9c94C395cBcBDe662235E0A9d3bB87Ad708561BA:4359", // DID of the device that produced the signal
      "valueType": "float64", // data type of the value field either float64 or string
      "value": 25 // Value of the signal
    }
  }
}
```

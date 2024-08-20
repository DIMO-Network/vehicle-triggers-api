# vehicle-events-api

## Overview
The DIMO Events API is being developed to deliver real-time vehicle event notifications via webhooks. It aims to provide scalable and flexible event handling using Kafka.

## Objectives
Real-Time Notifications
Scalable Event Processing
Customizable Webhook Configurations

Currently in the planning and development phase. Further documentation and features will be added as the project progresses.

## How To

To create a db:

`$ createdb -h localhost -p 5432 vehicle_events_api`
`$ psql -h localhost -p 5432 -d vehicle_events_api`
`# ALTER DATABASE vehicle_events_api OWNER TO dimo;`

To install goose CLI:
```bash
$ go install github.com/pressly/goose/v3/cmd/goose
export GOOSE_DRIVER=postgres
```

Add a migrations:
`$ goose -dir migrations create <migration_name> sql`

Migrate DB to latest:
`$ go run ./cmd/devices-api migrate`
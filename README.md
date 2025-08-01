# vehicle-triggers-api

## Overview

The DIMO Vehicle Triggers API is being developed to deliver real-time vehicle notifications via webhooks. It aims to provide scalable and flexible event handling using Kafka.

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

Add a migrations:
`$ make add-migration name=<migration_name>`

Migrate DB to latest:
`$ go run cmd/vehicle-triggers-api/main.go -migrate="only"`

To regenerate models:
`$ make generate-sqlboiler`

# Running locally

- in your postgres: `create database vehicle_events_api with owner dimo;` make sure you have user `dimo` setup in your local db.
- check to see if kafka is installed locally `brew services list`
- install kafka with brew services
- list all topics: `/opt/homebrew/opt/kafka/bin/kafka-topics --list --bootstrap-server localhost:9092`
- Create a topic:
- `/opt/homebrew/opt/kafka/bin/kafka-topics --create --topic topics.signals --bootstrap-server localhost:9092 --partitions 1 --replication-factor 1`
- Make sure to check settings.yaml to have the correct KAFKA_BROKERS and TOPIC name
- Pending: nee sample webhook config
- need example of sending a payload in the kafka topic

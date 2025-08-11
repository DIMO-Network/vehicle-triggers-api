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

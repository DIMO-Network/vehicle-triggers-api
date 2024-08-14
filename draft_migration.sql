-- +goose Up
-- +goose StatementBegin
SELECT 'up SQL query';

SET search_path = events_api, public;

CREATE TABLE IF NOT EXISTS webhooks (
                                        id BIGSERIAL NOT NULL,
                                        condition TEXT NOT NULL,
                                        flow_type TEXT NOT NULL,
                                        event_type TEXT NOT NULL,
                                        threshold_value FLOAT NOT NULL,
                                        target_uri TEXT NOT NULL,
                                        cooldown_period INTEGER NOT NULL DEFAULT 0,
                                        created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
                                        updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
                                        status TEXT NOT NULL,
                                        owner_dev_license_address BYTEA NOT NULL,

                                        CONSTRAINT webhooks_pkey PRIMARY KEY (id)
);

CREATE TABLE IF NOT EXISTS events (
                                      id BIGSERIAL NOT NULL,
                                      webhook_id BIGSERIAL REFERENCES webhooks(id) NOT NULL,
                                      snapshot_data TEXT NOT NULL,
                                      http_response_code INTEGER,
                                      created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,

                                      CONSTRAINT events_pkey PRIMARY KEY (id)
);

CREATE TABLE IF NOT EXISTS event_cooldowns (
                                               id BIGSERIAL NOT NULL,
                                               webhook_id BIGSERIAL REFERENCES webhooks(id) NOT NULL,
                                               last_triggered_at TIMESTAMP WITH TIME ZONE NOT NULL,

                                               CONSTRAINT event_cooldowns_pkey PRIMARY KEY (id)
);

CREATE TABLE IF NOT EXISTS webhook_vehicles (
                                                token_id NUMERIC(78) NOT NULL,
                                                webhook_id BIGSERIAL NOT NULL REFERENCES webhooks(id) NOT NULL,
                                                created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
                                                updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,

                                                CONSTRAINT webhook_vehicles_pkey PRIMARY KEY (token_id, webhook_id)
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT 'down SQL query';

DROP TABLE IF EXISTS webhook_vehicles;
DROP TABLE IF EXISTS event_cooldowns;
DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS webhooks;

-- +goose StatementEnd

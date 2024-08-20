-- +goose Up
-- +goose StatementBegin
SELECT 'up SQL query';

SET search_path = events_api, public;

CREATE TABLE IF NOT EXISTS events (
                                      id char(27) NOT NULL,
                                      condition TEXT NOT NULL,
                                      flow_type TEXT NOT NULL CHECK (flow_type IN ('Realtime', 'Batch')),
                                      event_name TEXT NOT NULL,
                                      webhook_target_uri TEXT NOT NULL,
                                      cooldown_period INTEGER NOT NULL DEFAULT 0,
                                      created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
                                      updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
                                      status TEXT NOT NULL CHECK (status IN ('Active', 'Inactive')),
                                      owner_dev_license_address BYTEA NOT NULL,

                                      CONSTRAINT events_pkey PRIMARY KEY (id)
);

-- figure out event retention policy
CREATE TABLE IF NOT EXISTS event_logs (
                                          id char(27) NOT NULL,
                                          vehicle_token_id NUMERIC(78) NOT NULL,
                                          webhook_id char(27) REFERENCES events(id) NOT NULL,
                                          snapshot_data TEXT NOT NULL,
                                          http_response_code INTEGER,
                                          last_triggered_at TIMESTAMP WITH TIME ZONE NOT NULL,
                                          created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,

                                          CONSTRAINT event_logs_pkey PRIMARY KEY (id)
);

CREATE TABLE IF NOT EXISTS webhook_vehicles (
                                                vehicle_token_id NUMERIC(78) NOT NULL,
                                                webhook_id char(27) NOT NULL REFERENCES events(id),
                                                created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
                                                updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,

                                                CONSTRAINT webhook_vehicles_pkey PRIMARY KEY (vehicle_token_id, webhook_id)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT 'down SQL query';

DROP TABLE IF EXISTS webhook_vehicles;
DROP TABLE IF EXISTS event_logs;
DROP TABLE IF EXISTS events;
-- +goose StatementEnd

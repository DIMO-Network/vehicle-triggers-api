-- +goose Up
-- +goose StatementBegin
SELECT 'up SQL query';

SET search_path = events_api, public;

CREATE TABLE IF NOT EXISTS developer_licenses (
      license_address BYTEA PRIMARY KEY,
      developer_id CHAR(27) NOT NULL,
      status TEXT NOT NULL CHECK (status IN ('Active', 'Inactive')),
      created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
      updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);


CREATE TABLE IF NOT EXISTS events (
      id CHAR(27) NOT NULL,
      service TEXT NOT NULL,
      data TEXT NOT NULL,
      trigger TEXT NOT NULL,
      setup TEXT NOT NULL CHECK (setup IN ('Realtime', 'Hourly', 'Daily')),
      parameters JSONB DEFAULT '{}'::JSONB,
      target_uri TEXT NOT NULL,
      cooldown_period INTEGER NOT NULL DEFAULT 0,
      developer_license_address BYTEA NOT NULL,
      created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
      updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
      status TEXT NOT NULL CHECK (status IN ('Active', 'Inactive')),

      CONSTRAINT events_pkey PRIMARY KEY (id),
      CONSTRAINT fk_dev_license FOREIGN KEY (developer_license_address)
      REFERENCES developer_licenses (license_address) ON DELETE CASCADE
);


CREATE TABLE IF NOT EXISTS event_logs (
      id CHAR(27) NOT NULL,
      vehicle_token_id NUMERIC(78) NOT NULL,
      event_id CHAR(27) NOT NULL REFERENCES events(id),
      snapshot_data JSONB NOT NULL,
      http_response_code INTEGER,
      last_triggered_at TIMESTAMP WITH TIME ZONE NOT NULL,
      condition_data JSONB DEFAULT '{}'::JSONB,
      event_type TEXT NOT NULL DEFAULT '',
      permission_status TEXT NOT NULL DEFAULT 'Granted',

      created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,

      CONSTRAINT event_logs_pkey PRIMARY KEY (id)
);

CREATE TABLE IF NOT EXISTS event_vehicles (
      vehicle_token_id NUMERIC(78) NOT NULL,
      event_id CHAR(27) NOT NULL REFERENCES events(id),
      developer_license_address BYTEA NOT NULL,
      created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
      updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,

      CONSTRAINT event_vehicles_pkey PRIMARY KEY (vehicle_token_id, event_id, developer_license_address),
      CONSTRAINT fk_vehicle_license FOREIGN KEY (developer_license_address)
      REFERENCES developer_licenses (license_address) ON DELETE CASCADE
);
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
SELECT 'down SQL query';

DROP TABLE IF EXISTS event_vehicles;
DROP TABLE IF EXISTS event_logs;
DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS developer_licenses;
-- +goose StatementEnd

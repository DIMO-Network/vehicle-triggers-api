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

DROP TABLE IF EXISTS webhook_vehicles;

ALTER TABLE events
    RENAME COLUMN webhook_target_uri TO target_uri,
    ADD COLUMN condition JSONB NOT NULL DEFAULT '{}'::JSONB,
    ADD COLUMN parameters JSONB DEFAULT '{}'::JSONB,
    ADD COLUMN developer_license_address BYTEA NOT NULL,
    ADD CONSTRAINT fk_dev_license FOREIGN KEY (developer_license_address)
    REFERENCES developer_licenses (license_address) ON DELETE CASCADE;

ALTER TABLE event_logs
    ADD COLUMN event_type TEXT NOT NULL DEFAULT '',
    ADD COLUMN condition_data JSONB DEFAULT '{}'::JSONB,
    ADD COLUMN permission_status TEXT NOT NULL DEFAULT 'Granted';

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

ALTER TABLE event_logs
    DROP COLUMN IF EXISTS event_type,
    DROP COLUMN IF EXISTS condition_data,
    DROP COLUMN IF EXISTS permission_status;

ALTER TABLE events
    RENAME COLUMN target_uri TO webhook_target_uri,
    DROP COLUMN IF EXISTS condition,
    DROP COLUMN IF EXISTS parameters,
    DROP COLUMN IF EXISTS developer_license_address;

DROP TABLE IF EXISTS developer_licenses;

CREATE TABLE IF NOT EXISTS webhook_vehicles (
    vehicle_token_id NUMERIC(78) NOT NULL,
    webhook_id CHAR(27) NOT NULL REFERENCES events(id),
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT webhook_vehicles_pkey PRIMARY KEY (vehicle_token_id, webhook_id)
);
-- +goose StatementEnd

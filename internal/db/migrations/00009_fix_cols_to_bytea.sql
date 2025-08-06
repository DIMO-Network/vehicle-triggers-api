-- +goose Up
-- +goose StatementBegin
SELECT 'up SQL query';

ALTER TABLE developer_licenses DROP COLUMN license_address_hex;
ALTER TABLE events DROP COLUMN developer_license_address_hex;
ALTER TABLE event_vehicles DROP COLUMN developer_license_address_hex;

ALTER TABLE developer_licenses ADD COLUMN license_address_hex BYTEA;
UPDATE developer_licenses
    SET license_address_hex = license_address;
ALTER TABLE developer_licenses ALTER COLUMN license_address_hex SET NOT NULL;

ALTER TABLE events ADD COLUMN developer_license_address_hex BYTEA;
UPDATE events
    SET developer_license_address_hex = developer_license_address;
ALTER TABLE events ALTER COLUMN developer_license_address_hex SET NOT NULL;

ALTER TABLE event_vehicles ADD COLUMN developer_license_address_hex BYTEA;
UPDATE event_vehicles
    SET developer_license_address_hex = developer_license_address;
ALTER TABLE event_vehicles ALTER COLUMN developer_license_address_hex SET NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT 'down SQL query';
ALTER TABLE event_vehicles DROP COLUMN developer_license_address_hex;
ALTER TABLE events DROP COLUMN developer_license_address_hex;
ALTER TABLE developer_licenses DROP COLUMN license_address_hex;

ALTER TABLE developer_licenses ADD COLUMN license_address_hex TEXT;
ALTER TABLE events ADD COLUMN developer_license_address_hex TEXT;
ALTER TABLE event_vehicles ADD COLUMN developer_license_address_hex TEXT;
-- +goose StatementEnd

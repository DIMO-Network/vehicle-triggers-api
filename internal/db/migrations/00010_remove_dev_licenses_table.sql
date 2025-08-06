-- +goose Up
-- +goose StatementBegin
SELECT 'up SQL query';

-- Drop foreign key constraints first
ALTER TABLE events DROP CONSTRAINT IF EXISTS fk_dev_license;
ALTER TABLE event_vehicles DROP CONSTRAINT IF EXISTS fk_vehicle_license;

-- Now drop the table
DROP TABLE IF EXISTS developer_licenses;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT 'down SQL query';

-- Recreate the developer_licenses table
CREATE TABLE IF NOT EXISTS developer_licenses (
      license_address BYTEA PRIMARY KEY,
      developer_id CHAR(27) NOT NULL,
      status TEXT NOT NULL CHECK (status IN ('Active', 'Inactive')),
      created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
      updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Recreate the foreign key constraints
ALTER TABLE events ADD CONSTRAINT fk_dev_license 
    FOREIGN KEY (developer_license_address) 
    REFERENCES developer_licenses (license_address) ON DELETE CASCADE;

ALTER TABLE event_vehicles ADD CONSTRAINT fk_vehicle_license 
    FOREIGN KEY (developer_license_address) 
    REFERENCES developer_licenses (license_address) ON DELETE CASCADE;

-- +goose StatementEnd

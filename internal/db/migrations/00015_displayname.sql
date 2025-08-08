-- +goose Up
-- +goose StatementBegin
SELECT 'up SQL query';
-- +goose StatementEnd

-- Add display_name column with a default unique-ish value to avoid insert conflicts before application sets it
ALTER TABLE triggers ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT md5(random()::text);
CREATE UNIQUE INDEX IF NOT EXISTS triggers_devaddr_display_name_uniq_ci
ON vehicle_events_api.triggers (developer_license_address, display_name)
WHERE status <> 'Deleted';

-- +goose Down
-- +goose StatementBegin
SELECT 'down SQL query';
-- +goose StatementEnd

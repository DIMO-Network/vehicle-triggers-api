-- +goose Up
-- +goose StatementBegin
SELECT 'up SQL query';
ALTER TABLE events
    DROP COLUMN parameters;

ALTER TABLE event_vehicles
    DROP COLUMN condition_data;

ALTER TABLE event_logs
    DROP COLUMN condition_data;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT 'down SQL query';

ALTER TABLE events
    ADD COLUMN parameters JSONB DEFAULT '{}'::JSONB;

ALTER TABLE event_vehicles
    ADD COLUMN condition_data JSONB DEFAULT '{}'::JSONB;

ALTER TABLE event_logs
    ADD COLUMN condition_data JSONB DEFAULT '{}'::JSONB;
-- +goose StatementEnd

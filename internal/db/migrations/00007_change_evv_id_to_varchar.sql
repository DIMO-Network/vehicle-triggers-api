-- +goose Up
-- +goose StatementBegin
SELECT 'up SQL query';

ALTER TABLE event_vehicles
    ALTER COLUMN event_id TYPE VARCHAR(27);
-- +goose StatementEnd


-- +goose Down
-- +goose StatementBegin
SELECT 'down SQL query';
ALTER TABLE event_vehicles
    ALTER COLUMN event_id TYPE CHAR(27);
-- +goose StatementEnd

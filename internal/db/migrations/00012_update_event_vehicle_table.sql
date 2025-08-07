-- +goose Up
-- +goose StatementBegin
SELECT 'up SQL query';


-- Rename the event_vehicles table to vehicle_subscriptions
ALTER TABLE event_vehicles RENAME TO vehicle_subscriptions;
ALTER TABLE vehicle_subscriptions RENAME COLUMN event_id TO trigger_id;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT 'down SQL query';

-- Rename the vehicle_subscriptions table back to event_vehicles
ALTER TABLE vehicle_subscriptions RENAME TO event_vehicles;
ALTER TABLE event_vehicles RENAME COLUMN trigger_id TO event_id;

-- +goose StatementEnd

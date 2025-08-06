-- +goose Up
-- +goose StatementBegin
ALTER TABLE event_vehicles
    ADD COLUMN condition_data JSONB DEFAULT '{}'::JSONB;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE event_vehicles
    DROP COLUMN condition_data;
-- +goose StatementEnd

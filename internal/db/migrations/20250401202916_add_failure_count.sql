-- +goose Up
-- +goose StatementBegin
SELECT 'up SQL query';

ALTER TABLE events
    ADD COLUMN failure_count INTEGER NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT 'down SQL query';

ALTER TABLE events
    DROP COLUMN failure_count;
-- +goose StatementEnd

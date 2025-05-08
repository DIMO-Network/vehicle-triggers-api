-- +goose Up
-- +goose StatementBegin
SELECT 'up SQL query';

ALTER TABLE events
    ALTER COLUMN id TYPE VARCHAR(27);
-- +goose StatementEnd


-- +goose Down
-- +goose StatementBegin
SELECT 'down SQL query';
ALTER TABLE events
    ALTER COLUMN id TYPE CHAR(27);
-- +goose StatementEnd

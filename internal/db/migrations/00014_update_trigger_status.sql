-- +goose Up
-- +goose StatementBegin
SELECT 'up SQL query';
-- +goose StatementEnd
ALTER TABLE triggers DROP CONSTRAINT events_status_check;
ALTER TABLE triggers ADD CONSTRAINT triggers_status_check CHECK (status IN ('enabled', 'disabled', 'failed', 'deleted'));
-- +goose Down
-- +goose StatementBegin
SELECT 'down SQL query';
-- +goose StatementEnd

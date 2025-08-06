-- +goose Up
-- +goose StatementBegin
SELECT 'up SQL query';


-- Rename the table from event_logs to trigger_logs
ALTER TABLE event_logs RENAME TO trigger_logs;

-- Rename the event_id column to trigger_id
ALTER TABLE trigger_logs RENAME COLUMN event_id TO trigger_id;

-- Change the id column type from CHAR(27) to UUID
ALTER TABLE trigger_logs ALTER COLUMN id TYPE UUID USING id::UUID;

-- Remove the event_type column
ALTER TABLE trigger_logs DROP COLUMN event_type;

-- Remove the permission_status column
ALTER TABLE trigger_logs DROP COLUMN permission_status;

-- Remove the request_response column
ALTER TABLE trigger_logs DROP COLUMN http_response_code;

-- Add failure_reason column to store error details when response code is not 200
ALTER TABLE trigger_logs ADD COLUMN failure_reason TEXT;


-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT 'down SQL query';


-- Remove the failure_reason column
ALTER TABLE trigger_logs DROP COLUMN failure_reason;

-- Add back the permission_status column
ALTER TABLE trigger_logs ADD COLUMN permission_status TEXT NOT NULL DEFAULT 'Granted';

-- Add back the event_type column
ALTER TABLE trigger_logs ADD COLUMN event_type TEXT NOT NULL DEFAULT '';

-- Change the id column type back to CHAR(27)
ALTER TABLE trigger_logs ALTER COLUMN id TYPE CHAR(27);

-- Rename the trigger_id column back to event_id
ALTER TABLE trigger_logs RENAME COLUMN trigger_id TO event_id;

-- Rename the table back to event_logs
ALTER TABLE trigger_logs RENAME TO event_logs;

-- +goose StatementEnd

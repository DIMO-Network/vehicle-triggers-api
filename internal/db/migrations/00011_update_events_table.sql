-- +goose Up
-- +goose StatementBegin
SELECT 'up SQL query';

-- Drop foreign key constraints first
ALTER TABLE event_logs DROP CONSTRAINT IF EXISTS event_logs_event_id_fkey;
ALTER TABLE event_vehicles DROP CONSTRAINT IF EXISTS event_vehicles_event_id_fkey;

-- Rename the events table to triggers
ALTER TABLE events RENAME TO triggers;

-- Remove the setup column from the triggers table
ALTER TABLE triggers DROP COLUMN setup;

-- Rename the trigger column to condition
ALTER TABLE triggers RENAME COLUMN trigger TO condition;

-- Rename the data column to metric_name
ALTER TABLE triggers RENAME COLUMN data TO metric_name;

-- Change the id column type from CHAR(27) to UUID
ALTER TABLE triggers ALTER COLUMN id TYPE UUID USING id::UUID;

-- Update foreign key columns to UUID type
ALTER TABLE event_logs ALTER COLUMN event_id TYPE UUID USING event_id::UUID;
ALTER TABLE event_vehicles ALTER COLUMN event_id TYPE UUID USING event_id::UUID;

-- Recreate foreign key constraints
ALTER TABLE event_logs ADD CONSTRAINT event_logs_event_id_fkey FOREIGN KEY (event_id) REFERENCES triggers(id);
ALTER TABLE event_vehicles ADD CONSTRAINT event_vehicles_event_id_fkey FOREIGN KEY (event_id) REFERENCES triggers(id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT 'down SQL query';

-- Drop foreign key constraints
ALTER TABLE event_vehicles DROP CONSTRAINT IF EXISTS event_vehicles_event_id_fkey;
ALTER TABLE event_logs DROP CONSTRAINT IF EXISTS event_logs_event_id_fkey;

-- Change foreign key columns back to CHAR(27)
ALTER TABLE event_vehicles ALTER COLUMN event_id TYPE CHAR(27);
ALTER TABLE event_logs ALTER COLUMN event_id TYPE CHAR(27);

-- Change the id column type back to CHAR(27)
ALTER TABLE triggers ALTER COLUMN id TYPE CHAR(27);

-- Rename the metric_name column back to data
ALTER TABLE triggers RENAME COLUMN metric_name TO data;

-- Rename the condition column back to trigger
ALTER TABLE triggers RENAME COLUMN condition TO trigger;

-- Add back the setup column to the triggers table
ALTER TABLE triggers ADD COLUMN setup TEXT NOT NULL CHECK (setup IN ('Realtime', 'Hourly', 'Daily'));

-- Rename the triggers table back to events
ALTER TABLE triggers RENAME TO events;

-- Recreate original foreign key constraints
ALTER TABLE event_logs ADD CONSTRAINT event_logs_event_id_fkey FOREIGN KEY (event_id) REFERENCES events(id);
ALTER TABLE event_vehicles ADD CONSTRAINT event_vehicles_event_id_fkey FOREIGN KEY (event_id) REFERENCES events(id);

-- +goose StatementEnd

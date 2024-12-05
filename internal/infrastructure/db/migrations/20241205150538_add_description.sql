-- +goose Up
-- Add a 'description' column to the 'events' table
ALTER TABLE events
    ADD COLUMN description TEXT;

-- +goose Down
-- Remove the 'description' column from the 'events' table
ALTER TABLE events
    DROP COLUMN description;

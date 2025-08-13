-- +goose Up
-- +goose StatementBegin

-- Add asset_did column to vehicle_subscriptions table
ALTER TABLE vehicle_subscriptions ADD COLUMN asset_did text;

-- Add asset_did column to trigger_logs table  
ALTER TABLE trigger_logs ADD COLUMN asset_did text;

-- Populate asset_did with string values from vehicle_token_id
UPDATE vehicle_subscriptions SET asset_did = vehicle_token_id::text;
UPDATE trigger_logs SET asset_did = vehicle_token_id::text;

-- Make asset_did NOT NULL after populating the data
ALTER TABLE vehicle_subscriptions ALTER COLUMN asset_did SET NOT NULL;
ALTER TABLE trigger_logs ALTER COLUMN asset_did SET NOT NULL;

-- Drop the primary key constraint from vehicle_subscriptions
ALTER TABLE vehicle_subscriptions DROP CONSTRAINT vehicle_subscriptions_pkey;

-- Drop the foreign key constraint from trigger_logs that references triggers
ALTER TABLE trigger_logs DROP CONSTRAINT trigger_logs_event_id_fkey;

-- Drop the old vehicle_token_id columns
ALTER TABLE vehicle_subscriptions DROP COLUMN vehicle_token_id;
ALTER TABLE trigger_logs DROP COLUMN vehicle_token_id;

-- Add new primary key constraint using asset_did instead of vehicle_token_id
ALTER TABLE vehicle_subscriptions ADD CONSTRAINT vehicle_subscriptions_pkey PRIMARY KEY (asset_did, trigger_id);

-- Re-add the foreign key constraint for trigger_logs
ALTER TABLE trigger_logs ADD CONSTRAINT trigger_logs_trigger_id_fkey FOREIGN KEY (trigger_id) REFERENCES triggers(id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Add back vehicle_token_id columns (initially nullable)
ALTER TABLE vehicle_subscriptions ADD COLUMN vehicle_token_id numeric(78,0);
ALTER TABLE trigger_logs ADD COLUMN vehicle_token_id numeric(78,0);

-- Populate vehicle_token_id from asset_did (convert string back to numeric)
UPDATE vehicle_subscriptions SET vehicle_token_id = asset_did::numeric(78,0);
UPDATE trigger_logs SET vehicle_token_id = asset_did::numeric(78,0);

-- Make vehicle_token_id NOT NULL after populating
ALTER TABLE vehicle_subscriptions ALTER COLUMN vehicle_token_id SET NOT NULL;
ALTER TABLE trigger_logs ALTER COLUMN vehicle_token_id SET NOT NULL;

-- Drop current primary key constraint
ALTER TABLE vehicle_subscriptions DROP CONSTRAINT vehicle_subscriptions_pkey;

-- Drop the foreign key constraint
ALTER TABLE trigger_logs DROP CONSTRAINT trigger_logs_event_id_fkey;

-- Drop asset_did columns
ALTER TABLE vehicle_subscriptions DROP COLUMN asset_did;
ALTER TABLE trigger_logs DROP COLUMN asset_did;

-- Restore original primary key constraint
ALTER TABLE vehicle_subscriptions ADD CONSTRAINT vehicle_subscriptions_pkey PRIMARY KEY (vehicle_token_id, trigger_id);

-- Restore original foreign key constraint
ALTER TABLE trigger_logs ADD CONSTRAINT trigger_logs_event_id_fkey FOREIGN KEY (trigger_id) REFERENCES triggers(id);

-- +goose StatementEnd

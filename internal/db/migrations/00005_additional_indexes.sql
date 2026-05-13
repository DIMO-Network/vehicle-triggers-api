-- +goose Up
-- +goose StatementBegin

CREATE INDEX IF NOT EXISTS idx_triggers_metric_name ON triggers USING btree (metric_name);
CREATE INDEX IF NOT EXISTS idx_vehicle_subs_trigger_id ON vehicle_subscriptions USING btree (trigger_id);

DROP INDEX IF EXISTS idx_trigger_logs_trigger_asset;
CREATE INDEX IF NOT EXISTS idx_trigger_logs_trigger_asset_time ON trigger_logs USING btree (trigger_id, asset_did, last_triggered_at DESC);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_triggers_metric_name;
DROP INDEX IF EXISTS idx_vehicle_subs_trigger_id;
DROP INDEX IF EXISTS idx_trigger_logs_trigger_asset_time;

-- +goose StatementEnd

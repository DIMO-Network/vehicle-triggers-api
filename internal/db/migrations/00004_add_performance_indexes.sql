-- +goose Up
-- +goose StatementBegin

-- Add composite index for trigger_logs query optimization
-- This index supports the query pattern: WHERE trigger_id = ? AND asset_did = ?
CREATE INDEX idx_trigger_logs_trigger_asset ON trigger_logs USING btree (trigger_id, asset_did);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX idx_trigger_logs_trigger_asset;

-- +goose StatementEnd

-- +goose Up
-- +goose StatementBegin

-- Remove developer_license_address_hex column from triggers table
ALTER TABLE triggers DROP COLUMN developer_license_address_hex;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Add back developer_license_address_hex column
ALTER TABLE triggers ADD COLUMN developer_license_address_hex bytea NOT NULL;

-- +goose StatementEnd

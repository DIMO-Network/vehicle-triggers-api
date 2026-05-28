-- +goose Up
-- +goose StatementBegin

-- Per-trigger HMAC signing secret. The webhook sender uses this to sign
-- (timestamp || body) so receivers can verify the request was issued by us
-- and hasn't been tampered with in flight. Nullable for backwards compat
-- with existing rows; new rows fill on create. Backfill is handled at
-- application startup (or via a one-off script) - existing webhooks will
-- continue to deliver unsigned until their owner rotates the secret.

ALTER TABLE triggers
  ADD COLUMN IF NOT EXISTS signing_secret VARCHAR(64);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE triggers
  DROP COLUMN IF EXISTS signing_secret;

-- +goose StatementEnd

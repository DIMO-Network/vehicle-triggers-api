-- +goose Up
-- +goose StatementBegin

-- Per-trigger HMAC signing secret. The webhook sender uses this to sign
-- (timestamp || body) so receivers can verify the request was issued by us
-- and hasn't been tampered with in flight. Nullable for backwards compat
-- with existing rows; new rows fill on create.
--
-- There is NO automatic backfill: pre-existing webhooks (signing_secret IS
-- NULL) continue to deliver UNSIGNED indefinitely until their owner calls
-- POST /v1/webhooks/:id/rotate-secret. If signed delivery is required for
-- all existing webhooks, run a one-off rotation over the affected rows as a
-- separate operational step.

ALTER TABLE triggers
  ADD COLUMN IF NOT EXISTS signing_secret VARCHAR(64);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE triggers
  DROP COLUMN IF EXISTS signing_secret;

-- +goose StatementEnd

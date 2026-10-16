-- +goose Up
-- +goose StatementBegin

-- The developer-license signers (API keys) whose JWTs created the webhook and last changed
-- its target URL, condition or subscribed vehicles, as lowercase hex. NULL when unknown:
-- before dex put signer_address on developer JWTs, or never changed. The DIMO console's
-- team removal review compares both to the signers a member was granted.
ALTER TABLE triggers
  ADD COLUMN IF NOT EXISTS created_by_signer TEXT,
  ADD COLUMN IF NOT EXISTS updated_by_signer TEXT;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE triggers
  DROP COLUMN IF EXISTS updated_by_signer,
  DROP COLUMN IF EXISTS created_by_signer;

-- +goose StatementEnd

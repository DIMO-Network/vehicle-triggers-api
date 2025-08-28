-- +goose Up
-- +goose StatementBegin
CREATE TABLE triggers (
    id uuid NOT NULL,
    service text NOT NULL,
    metric_name text NOT NULL,
    condition text NOT NULL,
    target_uri text NOT NULL,
    cooldown_period integer DEFAULT 0 NOT NULL,
    developer_license_address bytea NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    status text NOT NULL,
    description text,
    failure_count integer DEFAULT 0 NOT NULL,
    developer_license_address_hex bytea NOT NULL,
    display_name text DEFAULT md5((random())::text) NOT NULL,
    CONSTRAINT triggers_status_check CHECK ((status = ANY (ARRAY['enabled'::text, 'disabled'::text, 'failed'::text, 'deleted'::text]))),
    CONSTRAINT triggers_pkey PRIMARY KEY (id)
);

-- Unique index on developer_license_address and display_name when status is not 'deleted'
CREATE UNIQUE INDEX triggers_devaddr_display_name_uniq_ci ON triggers USING btree (developer_license_address, display_name) WHERE (status <> 'deleted'::text);


CREATE TABLE vehicle_subscriptions (
    vehicle_token_id numeric(78,0) NOT NULL,
    trigger_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT vehicle_subscriptions_pkey PRIMARY KEY (vehicle_token_id, trigger_id),
    CONSTRAINT vehicle_subscriptions_trigger_id_fkey FOREIGN KEY (trigger_id) REFERENCES triggers(id)
);


CREATE TABLE trigger_logs (
    id uuid NOT NULL,
    vehicle_token_id numeric(78,0) NOT NULL,
    trigger_id uuid NOT NULL,
    snapshot_data jsonb NOT NULL,
    last_triggered_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    failure_reason text,
    CONSTRAINT trigger_logs_pkey PRIMARY KEY (id),
    CONSTRAINT trigger_logs_event_id_fkey FOREIGN KEY (trigger_id) REFERENCES triggers(id)
);


-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE trigger_logs;
DROP TABLE triggers;
DROP TABLE vehicle_subscriptions;
-- +goose StatementEnd

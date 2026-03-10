-- +goose Up
-- +goose StatementBegin

-- Migrate service types
UPDATE triggers SET service = 'signals.vss' WHERE service = 'telemetry.signals';

-- Migrate event service AND metric names (PascalCase → category.camelCase)
UPDATE triggers SET
    service = CASE metric_name
        WHEN 'Collision' THEN 'events.safety'
        ELSE 'events.behavior'
    END,
    metric_name = CASE metric_name
        WHEN 'HarshBraking' THEN 'harshBraking'
        WHEN 'ExtremeBraking' THEN 'extremeBraking'
        WHEN 'HarshAcceleration' THEN 'harshAcceleration'
        WHEN 'HarshCornering' THEN 'harshCornering'
        WHEN 'Collision' THEN 'collision'
        ELSE metric_name
    END
WHERE service = 'telemetry.events';

-- Clear event trigger log snapshots so they regenerate with the new format.
-- Signal snapshots are unaffected (Signal struct shape is compatible).
-- This causes one "cold start" evaluation per event trigger per vehicle — the first event
-- after migration will have no previous value, which is safe (CEL treats missing previous
-- values as zero/empty, same as a brand new trigger).
DELETE FROM trigger_logs
WHERE trigger_id IN (
    SELECT id FROM triggers WHERE service IN ('events.behavior', 'events.safety')
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Reverse service types
UPDATE triggers SET service = 'telemetry.signals' WHERE service = 'signals.vss';

-- Reverse event service AND metric names
UPDATE triggers SET
    service = 'telemetry.events',
    metric_name = CASE metric_name
        WHEN 'harshBraking' THEN 'HarshBraking'
        WHEN 'extremeBraking' THEN 'ExtremeBraking'
        WHEN 'harshAcceleration' THEN 'HarshAcceleration'
        WHEN 'harshCornering' THEN 'HarshCornering'
        WHEN 'collision' THEN 'Collision'
        ELSE metric_name
    END
WHERE service IN ('events.behavior', 'events.safety');

-- +goose StatementEnd

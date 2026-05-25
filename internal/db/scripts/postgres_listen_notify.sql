-- ken LISTEN/NOTIFY event trigger (v0.8.0, ADR-020)
-- Installs a single schema-level event trigger that fires on tracked DDL
-- and emits pg_notify('ken_schema_changed', '<command_tag>:<object_identity>').
--
-- Run once after a fresh database setup (or any time the event trigger
-- needs to be re-created):
--   ken-mcp print-listen-script | psql $KEN_DB_DSN
--
-- Re-running is idempotent (drops then recreates).
-- Requires Postgres 9.3+ (event triggers were introduced in 9.3).

BEGIN;

DROP EVENT TRIGGER IF EXISTS ken_schema_changed_trigger;
DROP FUNCTION IF EXISTS ken_notify_schema_changed();

CREATE FUNCTION ken_notify_schema_changed()
RETURNS event_trigger
LANGUAGE plpgsql AS $$
DECLARE
    obj record;
    payload text;
BEGIN
    FOR obj IN SELECT * FROM pg_event_trigger_ddl_commands() LOOP
        payload := obj.command_tag || ':' || COALESCE(obj.object_identity, '');
        PERFORM pg_notify('ken_schema_changed', payload);
    END LOOP;
END;
$$;

CREATE EVENT TRIGGER ken_schema_changed_trigger
    ON ddl_command_end
    WHEN TAG IN (
        'CREATE TABLE', 'ALTER TABLE', 'DROP TABLE',
        'CREATE INDEX', 'ALTER INDEX', 'DROP INDEX',
        'CREATE VIEW', 'ALTER VIEW', 'DROP VIEW',
        'CREATE MATERIALIZED VIEW', 'ALTER MATERIALIZED VIEW', 'DROP MATERIALIZED VIEW',
        'CREATE FUNCTION', 'ALTER FUNCTION', 'DROP FUNCTION',
        'CREATE TRIGGER', 'ALTER TRIGGER', 'DROP TRIGGER',
        'CREATE TYPE', 'ALTER TYPE', 'DROP TYPE'
    )
    EXECUTE FUNCTION ken_notify_schema_changed();

COMMIT;

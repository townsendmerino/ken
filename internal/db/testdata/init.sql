-- Integration-test schema for internal/db. Loaded by integration_test.go
-- before every test run via DROP + CREATE so the test is hermetic against
-- whatever residue a prior run left in the database.
--
-- Covers: tables with PK / NOT NULL / DEFAULT / UNIQUE / FK, an index,
-- a view, a function, a non-public schema. The shapes hit every code
-- path in introspect.go / emit.go / sample.go.

DROP SCHEMA IF EXISTS ken_test CASCADE;
CREATE SCHEMA ken_test;

CREATE TABLE ken_test.users (
    id          BIGSERIAL PRIMARY KEY,
    email       VARCHAR(255) NOT NULL UNIQUE,
    role        VARCHAR(32)  NOT NULL DEFAULT 'guest',
    created_at  TIMESTAMP    NOT NULL DEFAULT NOW()
);

CREATE TABLE ken_test.sessions (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES ken_test.users(id) ON DELETE CASCADE,
    token       VARCHAR(64) NOT NULL UNIQUE,
    created_at  TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX users_email_idx ON ken_test.users (email);
CREATE INDEX sessions_user_id_idx ON ken_test.sessions (user_id);

CREATE VIEW ken_test.active_users AS
    SELECT u.id, u.email, COUNT(s.id) AS session_count
    FROM ken_test.users u
    LEFT JOIN ken_test.sessions s ON s.user_id = u.id
    GROUP BY u.id, u.email;

CREATE FUNCTION ken_test.greet(name TEXT) RETURNS TEXT
    LANGUAGE sql IMMUTABLE
    AS $$ SELECT 'hello, ' || name $$;

-- Seed deterministic rows so row-sampling determinism is testable.
INSERT INTO ken_test.users (id, email, role, created_at) VALUES
    (1,  'alice@example.com',  'admin',  '2024-01-15 00:00:00'),
    (2,  'bob@example.com',    'member', '2024-03-22 00:00:00'),
    (3,  'claire@example.com', 'guest',  '2025-11-08 00:00:00');

INSERT INTO ken_test.sessions (id, user_id, token, created_at) VALUES
    (10, 1, 'tok-alice-001', '2024-02-01 00:00:00'),
    (11, 2, 'tok-bob-001',   '2024-04-01 00:00:00');

-- Make reltuples non-zero for the sampling test's "of ~N" rendering.
ANALYZE ken_test.users;
ANALYZE ken_test.sessions;

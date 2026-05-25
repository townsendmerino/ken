-- Integration-test schema for MySQL. Loaded by mysql_integration_test.go
-- before every test run via DROP + CREATE so the test is hermetic.
--
-- Lives in its own DATABASE (ken_test) so KEN_DB_SCHEMAS / KEN_DB_EXCLUDE_SCHEMAS
-- filter tests have a clear known-name to allow / deny. Mirrors init.sql
-- shape-for-shape so the same chunk-shape assertions apply.

DROP DATABASE IF EXISTS ken_test;
CREATE DATABASE ken_test;
USE ken_test;

CREATE TABLE users (
    id          BIGINT          NOT NULL AUTO_INCREMENT PRIMARY KEY,
    email       VARCHAR(255)    NOT NULL UNIQUE,
    role        VARCHAR(32)     NOT NULL DEFAULT 'guest',
    created_at  DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE sessions (
    id          BIGINT          NOT NULL AUTO_INCREMENT PRIMARY KEY,
    user_id     BIGINT          NOT NULL,
    token       VARCHAR(64)     NOT NULL UNIQUE,
    created_at  DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_sessions_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX users_email_idx ON users (email);
CREATE INDEX sessions_user_id_idx ON sessions (user_id);

CREATE VIEW active_users AS
    SELECT u.id, u.email, COUNT(s.id) AS session_count
    FROM users u
    LEFT JOIN sessions s ON s.user_id = u.id
    GROUP BY u.id, u.email;

-- Seed deterministic rows so row-sampling determinism is testable.
INSERT INTO users (id, email, role, created_at) VALUES
    (1, 'alice@example.com',  'admin',  '2024-01-15 00:00:00'),
    (2, 'bob@example.com',    'member', '2024-03-22 00:00:00'),
    (3, 'claire@example.com', 'guest',  '2025-11-08 00:00:00');

INSERT INTO sessions (id, user_id, token, created_at) VALUES
    (10, 1, 'tok-alice-001', '2024-02-01 00:00:00'),
    (11, 2, 'tok-bob-001',   '2024-04-01 00:00:00');

ANALYZE TABLE users;
ANALYZE TABLE sessions;

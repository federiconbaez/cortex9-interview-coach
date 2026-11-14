-- Esquema inicial. SQL portable entre SQLite y Postgres (tipos TEXT/INTEGER/REAL,
-- fechas RFC 3339 de ancho fijo para que el orden lexicográfico sea cronológico).
CREATE TABLE auth_tokens (
    token_hash TEXT PRIMARY KEY,
    kind       TEXT NOT NULL,
    session_id TEXT,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    used       INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX auth_tokens_expires ON auth_tokens (expires_at);
CREATE TABLE scenarios (
    id         TEXT PRIMARY KEY,
    yaml       TEXT NOT NULL,
    json       TEXT NOT NULL,
    etag       TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE profile (
    id         INTEGER PRIMARY KEY,
    json       TEXT NOT NULL,
    etag       TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE sessions (
    id                 TEXT PRIMARY KEY,
    status             TEXT NOT NULL,
    mode               TEXT NOT NULL,
    scenario_id        TEXT,
    scenario_title     TEXT,
    lang               TEXT NOT NULL,
    input              TEXT NOT NULL,
    llm_provider       TEXT NOT NULL,
    llm_model          TEXT NOT NULL,
    max_followups      INTEGER NOT NULL,
    question_count     INTEGER NOT NULL,
    questions_answered INTEGER NOT NULL DEFAULT 0,
    ended_reason       TEXT,
    overall_score      REAL,
    config             TEXT NOT NULL,
    llm_errors         TEXT NOT NULL DEFAULT '[]',
    created_at         TEXT NOT NULL,
    started_at         TEXT,
    ended_at           TEXT
);
CREATE INDEX sessions_created ON sessions (created_at, id);
CREATE INDEX sessions_status ON sessions (status);
CREATE TABLE turns (
    session_id TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    idx        INTEGER NOT NULL,
    turn_id    TEXT NOT NULL,
    json       TEXT NOT NULL,
    PRIMARY KEY (session_id, idx)
);
CREATE TABLE reports (
    session_id TEXT PRIMARY KEY REFERENCES sessions (id) ON DELETE CASCADE,
    json       TEXT NOT NULL,
    markdown   TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE idempotency (
    scope      TEXT PRIMARY KEY,
    body_hash  TEXT NOT NULL,
    state      TEXT NOT NULL,
    status     INTEGER NOT NULL DEFAULT 0,
    headers    TEXT NOT NULL DEFAULT '{}',
    body       TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);

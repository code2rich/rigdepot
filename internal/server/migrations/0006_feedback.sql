-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Rigdepot Authors
--
-- Feedback queue and its timeline (package feedback).

CREATE TABLE feedback (
    id               TEXT    NOT NULL PRIMARY KEY,
    number           INTEGER NOT NULL UNIQUE,            -- shown as FB-42
    kind             TEXT    NOT NULL,
    origin           TEXT    NOT NULL,                   -- web | cli
    status           TEXT    NOT NULL,
    severity         TEXT    NOT NULL DEFAULT '',
    title            TEXT    NOT NULL,
    body             TEXT    NOT NULL DEFAULT '',
    author_id        TEXT    NOT NULL REFERENCES users (id),
    assignee_id      TEXT    REFERENCES users (id),
    resource_id      TEXT    REFERENCES resources (id),
    resource_version INTEGER,
    context          TEXT    NOT NULL DEFAULT '{}',      -- only the agreed fields, JSON
    score            INTEGER,
    created_at       BIGINT  NOT NULL,
    updated_at       BIGINT  NOT NULL,
    closed_at        BIGINT
);
CREATE INDEX feedback_status ON feedback (status, updated_at);
CREATE INDEX feedback_author ON feedback (author_id);
CREATE INDEX feedback_resource ON feedback (resource_id);

CREATE TABLE feedback_events (
    id          TEXT   NOT NULL PRIMARY KEY,
    feedback_id TEXT   NOT NULL REFERENCES feedback (id),
    at          BIGINT NOT NULL,
    actor_id    TEXT   REFERENCES users (id),            -- NULL for the system
    kind        TEXT   NOT NULL,                         -- comment | status | assign | score
    body        TEXT   NOT NULL DEFAULT '',
    data        TEXT   NOT NULL DEFAULT '{}'             -- JSON
);
CREATE INDEX feedback_events_feedback ON feedback_events (feedback_id, at);

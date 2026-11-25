-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Rigdepot Authors
--
-- Run-time settings (package settings) and background jobs (package jobs).

-- Settings edited on the web. Sensitive values are sealed with the master
-- key and stored encoded; the API never returns them.
CREATE TABLE settings (
    key        TEXT    NOT NULL PRIMARY KEY,
    value      TEXT    NOT NULL,                         -- JSON, or the sealed value when encrypted
    encrypted  BOOLEAN NOT NULL DEFAULT FALSE,
    updated_by TEXT    REFERENCES users (id),            -- NULL when set by the system
    updated_at BIGINT  NOT NULL
);

-- One row per job. The lease keeps a job to one instance at a time.
CREATE TABLE jobs (
    name        TEXT   NOT NULL PRIMARY KEY,
    last_run_at BIGINT,
    last_result TEXT   NOT NULL DEFAULT '',              -- ok | failed | timeout
    last_error  TEXT   NOT NULL DEFAULT '',
    next_run_at BIGINT,
    lease_owner TEXT   NOT NULL DEFAULT '',
    lease_until BIGINT
);

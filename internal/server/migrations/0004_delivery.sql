-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Rigdepot Authors
--
-- What each person turned on or off and what is installed where (package
-- delivery). Install reports carry no file paths.

CREATE TABLE user_resource_prefs (
    user_id     TEXT    NOT NULL REFERENCES users (id),
    resource_id TEXT    NOT NULL REFERENCES resources (id),
    enabled     BOOLEAN NOT NULL,
    updated_at  BIGINT  NOT NULL,
    PRIMARY KEY (user_id, resource_id)
);
CREATE INDEX user_resource_prefs_resource ON user_resource_prefs (resource_id);

CREATE TABLE installs (
    device_id   TEXT    NOT NULL REFERENCES devices (id),
    resource_id TEXT    NOT NULL REFERENCES resources (id),
    tool        TEXT    NOT NULL,                        -- adapter id, such as claude-code
    version     INTEGER NOT NULL,
    status      TEXT    NOT NULL,                        -- installed | failed | removed
    degraded    BOOLEAN NOT NULL DEFAULT FALSE,
    error_code  TEXT    NOT NULL DEFAULT '',             -- a stable code, never a message with paths
    reported_at BIGINT  NOT NULL,
    PRIMARY KEY (device_id, resource_id, tool)
);
CREATE INDEX installs_resource ON installs (resource_id, status);

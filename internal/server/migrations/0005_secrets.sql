-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Rigdepot Authors
--
-- Personal secrets (package secrets). Values are only ever stored sealed:
-- ciphertext under a per-record data key, the data key wrapped with a key
-- derived from the master key. Binary values are stored as base64 text.

CREATE TABLE secret_defs (
    name         TEXT   NOT NULL PRIMARY KEY,            -- the variable name, such as JIRA_TOKEN
    display_name TEXT   NOT NULL DEFAULT '',
    help_url     TEXT   NOT NULL DEFAULT '',             -- where to apply for one
    created_at   BIGINT NOT NULL,
    updated_at   BIGINT NOT NULL
);

CREATE TABLE resource_secrets (
    resource_id TEXT NOT NULL REFERENCES resources (id),
    secret_name TEXT NOT NULL REFERENCES secret_defs (name),
    PRIMARY KEY (resource_id, secret_name)
);
CREATE INDEX resource_secrets_secret ON resource_secrets (secret_name);

CREATE TABLE secret_values (
    user_id     TEXT    NOT NULL REFERENCES users (id),
    secret_name TEXT    NOT NULL REFERENCES secret_defs (name),
    ciphertext  TEXT    NOT NULL,
    nonce       TEXT    NOT NULL,
    wrapped_key TEXT    NOT NULL,                        -- the data key, wrapped
    key_id      TEXT    NOT NULL,                        -- which master key wrapped it
    version     INTEGER NOT NULL,                        -- bound into the additional data
    expires_at  BIGINT,
    updated_at  BIGINT  NOT NULL,
    PRIMARY KEY (user_id, secret_name)
);
CREATE INDEX secret_values_key ON secret_values (key_id);

-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Rigdepot Authors
--
-- Delivery (task M1-27).
--
-- device_syncs: what each computer said at its last sync. Only versions,
-- enumerations and tool ids: no paths, no names of files, nothing a person
-- wrote. user_id is the account the device belonged to when it reported.
--
-- installs.form: the form a degraded resource was delivered in (skill).
-- installs.status also takes skipped, besides installed, failed and removed.

CREATE TABLE device_syncs (
    device_id   TEXT    NOT NULL PRIMARY KEY REFERENCES devices (id),
    user_id     TEXT    NOT NULL REFERENCES users (id),
    cli_version TEXT    NOT NULL,
    os          TEXT    NOT NULL,
    arch        TEXT    NOT NULL,
    result      TEXT    NOT NULL,                     -- ok | partial | failed
    tools       TEXT    NOT NULL DEFAULT '[]',        -- JSON: [{tool, version}]
    finished_at BIGINT  NOT NULL,                     -- as the computer's clock said
    reported_at BIGINT  NOT NULL
);
CREATE INDEX device_syncs_user ON device_syncs (user_id);

ALTER TABLE installs ADD COLUMN form TEXT NOT NULL DEFAULT '';

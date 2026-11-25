-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Rigdepot Authors
--
-- In-product notifications and the mail outbox (package notify).

-- Messages are stored as a key and parameters and rendered in the reader's
-- language when read.
CREATE TABLE notifications (
    id          TEXT   NOT NULL PRIMARY KEY,
    user_id     TEXT   NOT NULL REFERENCES users (id),
    kind        TEXT   NOT NULL,                         -- event type, such as resource.published
    message_key TEXT   NOT NULL,
    params      TEXT   NOT NULL DEFAULT '{}',            -- JSON
    link        TEXT   NOT NULL DEFAULT '',
    created_at  BIGINT NOT NULL,
    read_at     BIGINT
);
CREATE INDEX notifications_user ON notifications (user_id, created_at);

CREATE TABLE mail_outbox (
    id              TEXT    NOT NULL PRIMARY KEY,
    recipient       TEXT    NOT NULL,
    template        TEXT    NOT NULL,
    locale          TEXT    NOT NULL,
    params          TEXT    NOT NULL DEFAULT '{}',       -- JSON
    status          TEXT    NOT NULL,                    -- queued | sent | failed
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at BIGINT  NOT NULL,
    last_error      TEXT    NOT NULL DEFAULT '',         -- passwords masked
    created_at      BIGINT  NOT NULL,
    sent_at         BIGINT
);
CREATE INDEX mail_outbox_due ON mail_outbox (status, next_attempt_at);

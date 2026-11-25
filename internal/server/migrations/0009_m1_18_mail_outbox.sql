-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Rigdepot Authors
--
-- The mail outbox (package notify): what the message, the retry window and
-- the hourly limit per recipient need beyond 0007.

ALTER TABLE mail_outbox ADD COLUMN recipient_name TEXT NOT NULL DEFAULT '';
ALTER TABLE mail_outbox ADD COLUMN link TEXT NOT NULL DEFAULT '';
-- The retry window counts from the first attempt, not from when the mail was
-- queued: mail queued while no mail server was configured still gets its
-- full window once one is.
ALTER TABLE mail_outbox ADD COLUMN first_attempt_at BIGINT;
-- Rows sent together as one message (a digest) share the batch, the ID of the
-- first of them; the hourly limit counts batches.
ALTER TABLE mail_outbox ADD COLUMN batch TEXT NOT NULL DEFAULT '';
CREATE INDEX mail_outbox_recipient_sent ON mail_outbox (recipient, sent_at);

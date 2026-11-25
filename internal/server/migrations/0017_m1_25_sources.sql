-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Rigdepot Authors
--
-- Resource sources (task M1-25): what the pull job needs besides 0003.

-- Counts every change of the row a person made; the source's ETag is made
-- from it. A pull does not change it.
ALTER TABLE sources ADD COLUMN revision BIGINT NOT NULL DEFAULT 0;
-- Someone asked for a pull ("pull now", or a new source): the job pulls it
-- on its next run even when it is not due, and clears this when it starts.
ALTER TABLE sources ADD COLUMN pull_requested_at BIGINT;
-- A pull in progress since then; NULL when none is running.
ALTER TABLE sources ADD COLUMN pull_started_at BIGINT;
-- What the last successful pull did: drafts created or updated, and
-- resources that could not be imported.
ALTER TABLE sources ADD COLUMN last_imported INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sources ADD COLUMN last_failed INTEGER NOT NULL DEFAULT 0;

-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Rigdepot Authors
--
-- Feedback (task M1-2E): what the queue needs besides 0006.

-- Why the handler declined it (wont_fix needs one), or the reason given with
-- the last status change.
ALTER TABLE feedback ADD COLUMN status_reason TEXT NOT NULL DEFAULT '';
-- What the submitter said with the score.
ALTER TABLE feedback ADD COLUMN score_note TEXT NOT NULL DEFAULT '';
CREATE INDEX feedback_assignee ON feedback (assignee_id);

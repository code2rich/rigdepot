-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Rigdepot Authors
--
-- How someone signed in (local, oidc, device_code, access_token…), for the
-- audit log's sign-in entries. The contract's AuditEntry has it as its own
-- field. Part of the chained content of a row (package audit).

ALTER TABLE audit_log ADD COLUMN method TEXT NOT NULL DEFAULT '';

-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Rigdepot Authors
--
-- Sign-in (task M1-20): when a local account last signed in with its
-- password, for the users page ("last sign-in"). Identity providers keep
-- theirs in identities.last_login_at.

ALTER TABLE local_credentials ADD COLUMN last_login_at BIGINT;

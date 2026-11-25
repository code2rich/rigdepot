-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Rigdepot Authors
--
-- Device code sign-in (task M1-22).
--
-- device_codes.key_storage: where the command line keeps the device key
-- (keyring | file), as the request said; it moves to the device when the
-- request is approved.
--
-- devices.issued_at: when the command line collected its device token. A
-- device is made when the person approves in the browser, with a
-- placeholder digest that no token matches; it gets its token's digest at
-- the next poll. Until then it is not signed in and not listed.

ALTER TABLE device_codes ADD COLUMN key_storage TEXT NOT NULL DEFAULT '';
ALTER TABLE devices ADD COLUMN issued_at BIGINT;

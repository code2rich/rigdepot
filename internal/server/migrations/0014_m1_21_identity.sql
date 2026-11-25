-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Rigdepot Authors
--
-- Identity sources (task M1-21, package authn): the sign-in methods an
-- administrator configured. The row with id 'local' holds the switch of the
-- built-in local accounts once someone changed it; without it they are on.
-- The client secret is sealed with the master key (masterkey.Sealed.Encode)
-- and never returned by the API.

CREATE TABLE identity_providers (
    id            TEXT    NOT NULL PRIMARY KEY,           -- 'local', or a UUIDv7
    kind          TEXT    NOT NULL,                       -- local, or an identity source kind such as oidc, gitlab
    name          TEXT    NOT NULL,                       -- shown on the login page
    enabled       BOOLEAN NOT NULL,
    position      INTEGER NOT NULL,                       -- order on the login page
    config        TEXT    NOT NULL DEFAULT '{}',          -- the settings without the secret, JSON
    client_secret TEXT    NOT NULL DEFAULT '',            -- sealed; '' when none is set
    revision      INTEGER NOT NULL DEFAULT 1,             -- for the ETag
    created_at    BIGINT  NOT NULL,
    updated_at    BIGINT  NOT NULL
);

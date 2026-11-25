-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Rigdepot Authors
--
-- Users, sign-in, devices, products and membership (packages access, authn).
-- Conventions of the portable subset (docs/architecture/06-server.md):
-- primary keys are TEXT UUIDv7 made by the application; times are BIGINT
-- UTC milliseconds; booleans are BOOLEAN; structures are JSON in TEXT.
-- Secrets and tokens are never stored, only their SHA-256 digests.

CREATE TABLE users (
    id             TEXT    NOT NULL PRIMARY KEY,
    username       TEXT    NOT NULL UNIQUE,             -- lower case
    display_name   TEXT    NOT NULL DEFAULT '',
    email          TEXT    NOT NULL DEFAULT '',          -- lower case; '' when unknown
    email_verified BOOLEAN NOT NULL DEFAULT FALSE,
    status         TEXT    NOT NULL,                     -- pending | active | disabled
    is_admin       BOOLEAN NOT NULL DEFAULT FALSE,       -- platform administrator
    job_title      TEXT    NOT NULL DEFAULT '',
    stages         TEXT    NOT NULL DEFAULT '[]',        -- stages the user follows, JSON array of stage codes
    locale         TEXT    NOT NULL DEFAULT '',          -- '' follows the browser
    created_at     BIGINT  NOT NULL,
    updated_at     BIGINT  NOT NULL,
    disabled_at    BIGINT
);
CREATE INDEX users_email ON users (email);

-- One row per identity provider account that signs in as the user.
CREATE TABLE identities (
    id            TEXT   NOT NULL PRIMARY KEY,
    user_id       TEXT   NOT NULL REFERENCES users (id),
    provider      TEXT   NOT NULL,                       -- identity provider id, such as oidc or gitlab
    subject       TEXT   NOT NULL,                       -- the provider's sub
    email         TEXT   NOT NULL DEFAULT '',
    created_at    BIGINT NOT NULL,
    last_login_at BIGINT,
    UNIQUE (provider, subject)
);
CREATE INDEX identities_user ON identities (user_id);

CREATE TABLE local_credentials (
    user_id         TEXT    NOT NULL PRIMARY KEY REFERENCES users (id),
    password_hash   TEXT    NOT NULL,                    -- argon2id, encoded with its parameters
    failed_attempts INTEGER NOT NULL DEFAULT 0,
    locked_until    BIGINT,
    updated_at      BIGINT  NOT NULL
);

CREATE TABLE products (
    id           TEXT   NOT NULL PRIMARY KEY,
    code         TEXT   NOT NULL UNIQUE,                 -- cannot change after creation
    name         TEXT   NOT NULL,
    product_line TEXT   NOT NULL DEFAULT '',
    created_at   BIGINT NOT NULL,
    updated_at   BIGINT NOT NULL
);

CREATE TABLE memberships (
    product_id TEXT   NOT NULL REFERENCES products (id),
    user_id    TEXT   NOT NULL REFERENCES users (id),
    role       TEXT   NOT NULL,                          -- owner | member
    created_at BIGINT NOT NULL,
    PRIMARY KEY (product_id, user_id)
);
CREATE INDEX memberships_user ON memberships (user_id);

-- One-time invitation links. Valid for 7 days.
CREATE TABLE invites (
    id               TEXT   NOT NULL PRIMARY KEY,
    token_digest     TEXT   NOT NULL UNIQUE,
    email            TEXT   NOT NULL,
    product_id       TEXT   REFERENCES products (id),    -- product to join, if any
    role             TEXT   NOT NULL DEFAULT 'member',
    invited_by       TEXT   NOT NULL REFERENCES users (id),
    created_at       BIGINT NOT NULL,
    expires_at       BIGINT NOT NULL,
    accepted_at      BIGINT,
    accepted_user_id TEXT   REFERENCES users (id),
    revoked_at       BIGINT
);
CREATE INDEX invites_email ON invites (email);

CREATE TABLE join_requests (
    id            TEXT   NOT NULL PRIMARY KEY,
    user_id       TEXT   NOT NULL REFERENCES users (id),
    product_id    TEXT   NOT NULL REFERENCES products (id),
    note          TEXT   NOT NULL DEFAULT '',
    status        TEXT   NOT NULL,                       -- pending | approved | rejected | withdrawn
    handled_by    TEXT   REFERENCES users (id),
    handled_at    BIGINT,
    reject_reason TEXT   NOT NULL DEFAULT '',
    created_at    BIGINT NOT NULL
);
CREATE INDEX join_requests_product ON join_requests (product_id, status);
CREATE INDEX join_requests_user ON join_requests (user_id);

-- Web sessions. No client address is stored.
CREATE TABLE sessions (
    id           TEXT   NOT NULL PRIMARY KEY,
    user_id      TEXT   NOT NULL REFERENCES users (id),
    token_digest TEXT   NOT NULL UNIQUE,
    created_at   BIGINT NOT NULL,
    last_seen_at BIGINT NOT NULL,                        -- idle expiry counts from here
    expires_at   BIGINT NOT NULL,                        -- absolute expiry
    revoked_at   BIGINT
);
CREATE INDEX sessions_user ON sessions (user_id);
CREATE INDEX sessions_expires ON sessions (expires_at);

-- Tokens for programs (CI). Only the digest and the first characters are kept.
CREATE TABLE access_tokens (
    id           TEXT   NOT NULL PRIMARY KEY,
    user_id      TEXT   NOT NULL REFERENCES users (id),
    name         TEXT   NOT NULL,
    token_digest TEXT   NOT NULL UNIQUE,
    token_prefix TEXT   NOT NULL,
    scope        TEXT   NOT NULL,                        -- read | publish
    created_at   BIGINT NOT NULL,
    expires_at   BIGINT,
    last_used_at BIGINT,
    revoked_at   BIGINT
);
CREATE INDEX access_tokens_user ON access_tokens (user_id);

-- Computers signed in with the command-line program.
CREATE TABLE devices (
    id               TEXT   NOT NULL PRIMARY KEY,
    user_id          TEXT   NOT NULL REFERENCES users (id),
    name             TEXT   NOT NULL,
    os               TEXT   NOT NULL,
    arch             TEXT   NOT NULL DEFAULT '',
    cli_version      TEXT   NOT NULL DEFAULT '',
    public_key       TEXT   NOT NULL,                    -- the device's public key, encoded
    token_digest     TEXT   NOT NULL UNIQUE,
    key_storage      TEXT   NOT NULL DEFAULT '',         -- keychain | file
    collection_scope TEXT   NOT NULL DEFAULT '',         -- kind of collection scope reported by the device
    created_at       BIGINT NOT NULL,
    last_seen_at     BIGINT NOT NULL,
    revoked_at       BIGINT
);
CREATE INDEX devices_user ON devices (user_id);

-- Pending device code sign-ins (RFC 8628). Expire after 10 minutes.
CREATE TABLE device_codes (
    id                 TEXT    NOT NULL PRIMARY KEY,
    device_code_digest TEXT    NOT NULL UNIQUE,
    user_code          TEXT    NOT NULL UNIQUE,
    device_name        TEXT    NOT NULL,
    os                 TEXT    NOT NULL,
    arch               TEXT    NOT NULL DEFAULT '',
    cli_version        TEXT    NOT NULL DEFAULT '',
    public_key         TEXT    NOT NULL,
    status             TEXT    NOT NULL,                 -- pending | approved | denied | issued
    user_id            TEXT    REFERENCES users (id),
    device_id          TEXT    REFERENCES devices (id),
    interval_seconds   INTEGER NOT NULL,
    created_at         BIGINT  NOT NULL,
    expires_at         BIGINT  NOT NULL,
    last_polled_at     BIGINT
);
CREATE INDEX device_codes_expires ON device_codes (expires_at);

-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Rigdepot Authors
--
-- Resources, drafts, versions and where they come from (package catalog).

-- Resource repositories. The read-only credential is sealed with the master
-- key (envelope encryption) and stored encoded; '' when there is none.
CREATE TABLE sources (
    id              TEXT    NOT NULL PRIMARY KEY,
    name            TEXT    NOT NULL,
    url             TEXT    NOT NULL,
    branch          TEXT    NOT NULL DEFAULT '',         -- '' is the remote's default branch
    dir             TEXT    NOT NULL DEFAULT '',         -- subdirectory with the resources
    credential      TEXT    NOT NULL DEFAULT '',
    position        INTEGER NOT NULL DEFAULT 0,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    last_fetch_at   BIGINT,
    last_fetch_ok   BOOLEAN,
    last_error      TEXT    NOT NULL DEFAULT '',
    last_commit     TEXT    NOT NULL DEFAULT '',
    last_success_at BIGINT,
    created_at      BIGINT  NOT NULL,
    updated_at      BIGINT  NOT NULL
);

-- Stages of work (design, coding, review…) that resources are tagged with.
CREATE TABLE stages (
    id         TEXT    NOT NULL PRIMARY KEY,
    code       TEXT    NOT NULL UNIQUE,
    name       TEXT    NOT NULL,
    position   INTEGER NOT NULL DEFAULT 0,
    hidden     BOOLEAN NOT NULL DEFAULT FALSE,
    created_at BIGINT  NOT NULL,
    updated_at BIGINT  NOT NULL
);

CREATE TABLE resources (
    id               TEXT    NOT NULL PRIMARY KEY,
    code             TEXT    NOT NULL UNIQUE,
    kind             TEXT    NOT NULL,
    origin           TEXT    NOT NULL,                   -- repo | upload | web
    source_id        TEXT    REFERENCES sources (id),
    scope_kind       TEXT    NOT NULL,                   -- org | product | me
    scope_product_id TEXT    REFERENCES products (id),
    owner_user_id    TEXT    REFERENCES users (id),      -- when scope_kind = 'me'
    policy           TEXT    NOT NULL,                   -- required | recommended | optional
    audience         TEXT    NOT NULL,                   -- rd | mgmt
    status           TEXT    NOT NULL,                   -- draft | published | disabled
    current_version  INTEGER,                            -- the version in use; NULL for a draft
    name             TEXT    NOT NULL,
    summary          TEXT    NOT NULL DEFAULT '',
    overrides        TEXT    NOT NULL DEFAULT '[]',      -- fields overridden on the platform, JSON array
    created_at       BIGINT  NOT NULL,
    updated_at       BIGINT  NOT NULL
);
CREATE INDEX resources_scope ON resources (scope_kind, scope_product_id);
CREATE INDEX resources_source ON resources (source_id);

CREATE TABLE resource_stages (
    resource_id TEXT NOT NULL REFERENCES resources (id),
    stage_id    TEXT NOT NULL REFERENCES stages (id),
    PRIMARY KEY (resource_id, stage_id)
);

CREATE TABLE resource_maintainers (
    resource_id TEXT NOT NULL REFERENCES resources (id),
    user_id     TEXT NOT NULL REFERENCES users (id),
    PRIMARY KEY (resource_id, user_id)
);
CREATE INDEX resource_maintainers_user ON resource_maintainers (user_id);

-- Resource bundles, addressed by their sha256. The one table whose column
-- type the portable subset cannot express.
-- +rgd dialect sqlite
CREATE TABLE blobs (
    digest     TEXT    NOT NULL PRIMARY KEY,
    size       INTEGER NOT NULL,
    data       BLOB    NOT NULL,
    created_at BIGINT  NOT NULL
);
-- +rgd dialect postgres
CREATE TABLE blobs (
    digest     TEXT    NOT NULL PRIMARY KEY,
    size       INTEGER NOT NULL,
    data       BYTEA   NOT NULL,
    created_at BIGINT  NOT NULL
);
-- +rgd end

-- At most one draft per resource.
CREATE TABLE resource_drafts (
    resource_id   TEXT    NOT NULL PRIMARY KEY REFERENCES resources (id),
    digest        TEXT    NOT NULL,                      -- content digest
    bundle        TEXT    NOT NULL REFERENCES blobs (digest),
    meta          TEXT    NOT NULL,                      -- metadata, JSON
    ruleset       INTEGER NOT NULL,                      -- version of the check rules used
    findings      TEXT    NOT NULL,                      -- check results, JSON
    source_commit TEXT    NOT NULL DEFAULT '',
    updated_by    TEXT    REFERENCES users (id),         -- NULL when imported from a repository
    updated_at    BIGINT  NOT NULL
);

-- Published versions. Rows are only ever inserted.
CREATE TABLE resource_versions (
    resource_id   TEXT    NOT NULL REFERENCES resources (id),
    number        INTEGER NOT NULL,
    digest        TEXT    NOT NULL,
    bundle        TEXT    NOT NULL REFERENCES blobs (digest),
    meta          TEXT    NOT NULL,                      -- metadata at the time of publishing, JSON
    ruleset       INTEGER NOT NULL,
    findings      TEXT    NOT NULL,
    source_commit TEXT    NOT NULL DEFAULT '',
    rollback_of   INTEGER,                               -- a rollback: the version the content came from
    note          TEXT    NOT NULL DEFAULT '',
    published_by  TEXT    NOT NULL REFERENCES users (id),
    published_at  BIGINT  NOT NULL,
    PRIMARY KEY (resource_id, number)
);

-- What workflows and agents refer to, by code (the target may not exist yet).
CREATE TABLE resource_refs (
    resource_id TEXT NOT NULL REFERENCES resources (id),
    ref_code    TEXT NOT NULL,
    PRIMARY KEY (resource_id, ref_code)
);
CREATE INDEX resource_refs_target ON resource_refs (ref_code);

-- False positives marked on check findings. A change of the line content
-- changes its digest, so the mark no longer matches.
CREATE TABLE check_waivers (
    id          TEXT   NOT NULL PRIMARY KEY,
    resource_id TEXT   NOT NULL REFERENCES resources (id),
    rule        TEXT   NOT NULL,
    file        TEXT   NOT NULL,
    line_digest TEXT   NOT NULL,
    reason      TEXT   NOT NULL,
    created_by  TEXT   NOT NULL REFERENCES users (id),
    created_at  BIGINT NOT NULL,
    UNIQUE (resource_id, rule, file, line_digest)
);

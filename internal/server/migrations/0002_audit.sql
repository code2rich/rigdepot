-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Rigdepot Authors
--
-- The audit log (package audit). Append-only: rows are chained with a keyed
-- hash, and the database itself refuses UPDATE and DELETE, so that a bug in
-- the program cannot change history. See docs/architecture/06-server.md#审计.

CREATE TABLE audit_log (
    seq          BIGINT NOT NULL PRIMARY KEY,            -- 1, 2, 3… without gaps
    at           BIGINT NOT NULL,
    category     TEXT   NOT NULL,                        -- platform | login
    actor_id     TEXT,                                   -- NULL for the system itself
    origin       TEXT   NOT NULL,                        -- web | cli | system
    via_tool     TEXT   NOT NULL DEFAULT '',
    device_id    TEXT   NOT NULL DEFAULT '',
    action       TEXT   NOT NULL,                        -- such as resource.publish
    object_kind  TEXT   NOT NULL,
    object_id    TEXT   NOT NULL,
    object_label TEXT   NOT NULL,                        -- the object's name at the time
    change       TEXT   NOT NULL DEFAULT '{}',           -- summary of the change, JSON; no secrets, no content
    result       TEXT   NOT NULL,                        -- ok | denied | pending
    reason       TEXT   NOT NULL DEFAULT '',
    prev_hash    TEXT   NOT NULL,
    hash         TEXT   NOT NULL
);
CREATE INDEX audit_log_at ON audit_log (at);
CREATE INDEX audit_log_actor ON audit_log (actor_id, at);
CREATE INDEX audit_log_object ON audit_log (object_kind, object_id);
CREATE INDEX audit_log_action ON audit_log (action, at);

-- +rgd dialect sqlite
CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON audit_log
BEGIN
    SELECT RAISE(ABORT, 'audit_log is append-only: UPDATE is not allowed');
END;
CREATE TRIGGER audit_log_no_delete BEFORE DELETE ON audit_log
BEGIN
    SELECT RAISE(ABORT, 'audit_log is append-only: DELETE is not allowed');
END;
-- +rgd dialect postgres
CREATE FUNCTION audit_log_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only: % is not allowed', TG_OP
        USING ERRCODE = 'insufficient_privilege';
END;
$$;
CREATE TRIGGER audit_log_no_change BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_append_only();
CREATE TRIGGER audit_log_no_truncate BEFORE TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_append_only();
-- +rgd end

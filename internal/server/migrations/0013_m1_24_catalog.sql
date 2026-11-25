-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Rigdepot Authors
--
-- The catalog (task M1-24): what a resource carries besides 0003, and the
-- version table made insert-only by the database itself.

-- Counts every change of the resource row; the resource's ETag is made from
-- it, so two changes within one millisecond still give two ETags.
ALTER TABLE resources ADD COLUMN revision BIGINT NOT NULL DEFAULT 0;
-- The metadata in force, JSON: the published (or, before the first publish,
-- the draft's) metadata with the platform overrides applied. The columns
-- name, summary, scope_*, policy and audience repeat what is queried.
ALTER TABLE resources ADD COLUMN meta TEXT NOT NULL DEFAULT '{}';
-- For each overridden field, the repository's value when it was overridden,
-- JSON: a later change in the repository is reported, not applied.
ALTER TABLE resources ADD COLUMN override_base TEXT NOT NULL DEFAULT '{}';
-- Where in its source repository the resource is ('' when uploaded or
-- registered on the web).
ALTER TABLE resources ADD COLUMN source_path TEXT NOT NULL DEFAULT '';
ALTER TABLE resources ADD COLUMN disabled_reason TEXT NOT NULL DEFAULT '';

-- Published versions are never changed or removed; a rollback inserts a new
-- one. As with the audit log, the database refuses UPDATE and DELETE so that
-- a bug cannot rewrite what people were given.
-- +rgd dialect sqlite
CREATE TRIGGER resource_versions_no_update BEFORE UPDATE ON resource_versions
BEGIN
    SELECT RAISE(ABORT, 'resource_versions is insert-only: UPDATE is not allowed');
END;
CREATE TRIGGER resource_versions_no_delete BEFORE DELETE ON resource_versions
BEGIN
    SELECT RAISE(ABORT, 'resource_versions is insert-only: DELETE is not allowed');
END;
-- +rgd dialect postgres
CREATE FUNCTION resource_versions_insert_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'resource_versions is insert-only: % is not allowed', TG_OP
        USING ERRCODE = 'insufficient_privilege';
END;
$$;
CREATE TRIGGER resource_versions_no_change BEFORE UPDATE OR DELETE ON resource_versions
    FOR EACH ROW EXECUTE FUNCTION resource_versions_insert_only();
CREATE TRIGGER resource_versions_no_truncate BEFORE TRUNCATE ON resource_versions
    FOR EACH STATEMENT EXECUTE FUNCTION resource_versions_insert_only();
-- +rgd end

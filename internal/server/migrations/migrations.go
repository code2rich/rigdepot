// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package migrations

import (
	"context"
	"embed"
	"io/fs"

	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// Namespace is the migration namespace of the core tables. Optional modules
// use their own identifiers.
const Namespace = "core"

//go:embed *.sql
var files embed.FS

// FS returns the migration files.
func FS() fs.FS { return files }

// Apply runs the core migrations that have not run yet and returns them.
func Apply(ctx context.Context, db *dbx.DB) ([]dbx.Migration, error) {
	return db.Migrate(ctx, Namespace, files)
}

// Pending returns the core migrations that have not run yet.
func Pending(ctx context.Context, db *dbx.DB) ([]dbx.Migration, error) {
	return db.Pending(ctx, Namespace, files)
}

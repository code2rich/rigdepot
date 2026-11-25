// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package dbx

// SetBeforeMigrationTx installs a function that Migrate calls right before
// it opens the transaction for each migration, and returns a function that
// removes it. Tests use it to line two migrating instances up on the same
// migration.
func SetBeforeMigrationTx(fn func(version int)) (restore func()) {
	old := beforeMigrationTx
	beforeMigrationTx = fn
	return func() { beforeMigrationTx = old }
}

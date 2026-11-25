// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package bundledenv is the check rule for configuration files with real values shipped inside a
// resource. Must fix.
//
// A resource may declare its local settings in .env.example; the real values live on each
// person's computer. A .env file, or .env.local, .env.production and the like, in a resource
// would hand one person's values, often credentials, to everyone. .env.example, .env.sample and
// .env.template are fine.
//
// Message keys, all under check.bundledenv.:
//
//	found  a .env file in the resource    {file}
package bundledenv

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package personalpath is the check rule for content that only holds on one computer. Advisory.
//
// A home directory with a person's name in it (/Users/<name>/, /home/<name>/,
// C:\Users\<name>\) or a service on a local port (localhost:<port>) works for the author and
// for nobody else. Write ~, $HOME or a setting in .env.example instead. Placeholders such as
// /Users/<你的用户名>/ and shared directories such as /Users/Shared/ are fine.
//
// A finding does not repeat the name it found: it is a person's account name.
//
// Message keys, all under check.personalpath.:
//
//	home_dir    a home directory with an account name in it    {system}  (macos, linux or windows)
//	local_port  a service on localhost or 127.0.0.1            {port}
package personalpath

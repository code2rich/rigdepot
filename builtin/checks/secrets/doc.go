// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package secrets is the check rule for credentials written into a resource. Must fix.
//
// It looks at every line of every text file for three things: credentials with a known shape
// (private keys, AWS access keys, GitHub, GitLab and Slack tokens, API keys that start with
// "sk-", Google API keys, JSON web tokens); settings whose name says the value is a credential
// (password=, api_key: and the like) with a value that is not a placeholder; and long strings
// with mixed case, digits and the entropy of random text. References such as ${secret:NAME} and
// placeholders such as <占位：令牌> are not credentials.
//
// A finding never contains what it matched: only the kind of credential, the fixed public prefix
// of its format and its length, or the name of the setting.
//
// Message keys, all under check.secrets.:
//
//	token_like    a credential with a known shape    {kind, masked, length}
//	assignment    a credential in a named setting    {key, length}
//	high_entropy  a long random-looking string       {length}
package secrets

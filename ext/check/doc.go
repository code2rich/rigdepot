// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package check is the extension point for intake check rules: the Rule interface, findings and
// rule sets. Version: v0.
//
// Every resource is checked before it can be published: rgd check on the author's computer and
// the server on import and upload run the same rules through the same engine. The rules are
// deterministic and use no AI. They exist to catch what must never reach other people's
// computers, such as a credential left in a skill or a command that wipes a disk, and to point
// out what makes a resource work badly.
//
// # A rule is a pure function
//
// A rule receives one resource, as an Input, and returns what it found. It cannot read anything
// else from disk, cannot use the network and cannot run a command: nothing in this package gives
// it the means, and a rule must not reach for them some other way.
//
// # Two levels
//
// A MustFix finding blocks publishing; an Advisory finding is shown and blocks nothing. Whether
// a rule blocks is a product decision, so a new rule starts as Advisory.
//
// # Findings never carry what they found
//
// A finding about a credential or any other sensitive content must not contain the matched
// text, not in Params and not in the name of a message key. Say what kind of thing it is and
// where; findings are stored, reported to the server and shown to other people.
//
// # Adding a rule
//
// A built-in rule is one directory under builtin/checks with New exported, samples that must be
// hit and samples that look similar but must not be, its texts in every required language, and
// one line in builtin/all.
//
// # Stability
//
// Everything under ext/ is public API. Before 1.0 it may change in breaking ways, and every such
// change is listed in the release notes. From 1.0 on, a breaking change means a new version of
// the package, and the old one stays for at least one major release.
package check

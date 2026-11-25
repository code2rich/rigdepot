// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package injection is the check rule for prompt injection in a resource. Must fix.
//
// What an AI reads can steer it. This rule looks at every line of every text file for: text that
// tells the AI to ignore its earlier instructions; characters a person cannot see, such as zero
// width spaces and bidirectional controls, which hide text from review; and requests to send
// credentials or key files somewhere. It recognises English and Chinese wording. A sentence that
// forbids the thing ("不要把令牌发到群里", "never send tokens to …") is not reported.
//
// Message keys, all under check.injection.:
//
//	ignore_instructions  asks to ignore or override earlier instructions
//	hidden_char          an invisible or bidirectional control character      {codepoint}
//	exfiltrate           asks to send credentials or key files somewhere
package injection

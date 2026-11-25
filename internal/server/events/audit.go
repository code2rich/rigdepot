// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package events

// The events of the audit log's daily check. The check runs in the server's assembly, which no
// domain package may import, so its event lives here.

// AuditChainBroken: the daily check of the audit log found the hash chain broken at Seq; the
// rows up to IntactThrough are intact. Published by every run that finds it broken, so once a
// day until the log is restored.
type AuditChainBroken struct {
	Seq           int64  `json:"seq"`
	IntactThrough int64  `json:"intact_through"`
	Problem       string `json:"problem"` // a short description in English, for logs
}

// EventName implements Payload.
func (AuditChainBroken) EventName() string { return "audit.chain_broken" }

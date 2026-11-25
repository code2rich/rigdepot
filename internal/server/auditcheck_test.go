// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/jobs"
)

// TestAuditCheckIsIncremental: each run checks the rows added since the last one, keeps how far
// it got, and a row changed after that makes the check fail and /readyz report the chain as
// degraded, on every later run too.
func TestAuditCheckIsIncremental(t *testing.T) {
	f := newAPI(t, false)
	f.app.AddReadyCheck(ReadyCheck{Name: "audit_chain", Check: f.app.auditChainReady})
	ctx := context.Background()
	admin := access.Actor{UserID: testAdmin, Origin: access.OriginWeb}
	product := func(code string) {
		t.Helper()
		if _, err := f.app.access.CreateProduct(ctx, admin, access.NewProduct{Code: code, Name: code}); err != nil {
			t.Fatal(err)
		}
	}
	lastSeq := func() int64 {
		t.Helper()
		var n int64
		if err := f.app.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM audit_log`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	product("shop")
	if err := f.app.checkAudit(ctx); err != nil {
		t.Fatal(err)
	}
	c, found, err := f.app.settings.AuditCheck(ctx)
	if err != nil || !found || c.LastSeq != lastSeq() || c.LastSeq == 0 || c.LastHash == "" || c.Broken != "" || c.At.IsZero() {
		t.Fatalf("after the first check: %+v %v %v (last row #%d)", c, found, err, lastSeq())
	}
	first := c.LastSeq

	product("pay")
	if err := f.app.checkAudit(ctx); err != nil {
		t.Fatal(err)
	}
	if c, _, _ = f.app.settings.AuditCheck(ctx); c.LastSeq != lastSeq() || c.LastSeq <= first {
		t.Fatalf("after the second check: %+v (last row #%d)", c, lastSeq())
	}
	if got := readyChecks(t, f)["audit_chain"]; got != "ok" {
		t.Errorf("/readyz audit_chain while intact: %q", got)
	}
	checked := c.LastSeq

	// Someone with access to the database changes a row added since.
	product("app")
	tamper(t, f, `UPDATE audit_log SET object_label = 'renamed' WHERE seq = (SELECT MAX(seq) FROM audit_log)`)
	for range 2 {
		if err := f.app.checkAudit(ctx); !errors.Is(err, errAuditBroken) {
			t.Fatalf("check after a row was changed: %v", err)
		}
		c, _, _ = f.app.settings.AuditCheck(ctx)
		if c.Broken == "" || c.BrokenSeq != lastSeq() || c.LastSeq != checked {
			t.Errorf("kept outcome: %+v", c)
		}
	}
	rec, _ := f.do(t, "GET", "/readyz", "", "", nil)
	if got := readyChecks(t, f)["audit_chain"]; got != "degraded" || rec.Code != 200 {
		t.Errorf("/readyz audit_chain once broken: %q (%d)", got, rec.Code)
	}
}

// TestAuditCheckIsScheduled: serve's background runs the check as the job audit-verify.
func TestAuditCheckIsScheduled(t *testing.T) {
	f := newAPIWith(t, fixtureOptions{background: true})
	ctx := context.Background()
	deadline := time.Now().Add(10 * time.Second)
	for {
		statuses, err := f.app.bg.Jobs.Statuses(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var result jobs.Result
		for _, s := range statuses {
			if s.Name == auditCheckJob {
				result = s.LastResult
			}
		}
		if result == jobs.OK {
			break
		}
		if result != "" || time.Now().After(deadline) {
			t.Fatalf("job %s: %q", auditCheckJob, result)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, found, err := f.app.settings.AuditCheck(ctx); !found || err != nil {
		t.Errorf("the job kept no outcome: %v %v", found, err)
	}
	if got := readyChecks(t, f)["audit_chain"]; got != "ok" {
		t.Errorf("/readyz audit_chain: %q", got)
	}
}

// tamper runs statements as someone with full access to the SQLite database would: around the
// triggers that refuse changes to the audit log.
func tamper(t *testing.T, f *apiFixture, statements ...string) {
	t.Helper()
	ctx := context.Background()
	for _, q := range append(append([]string{`DROP TRIGGER audit_log_no_update`, `DROP TRIGGER audit_log_no_delete`}, statements...),
		`CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON audit_log BEGIN SELECT RAISE(ABORT, 'no'); END`,
		`CREATE TRIGGER audit_log_no_delete BEFORE DELETE ON audit_log BEGIN SELECT RAISE(ABORT, 'no'); END`) {
		if _, err := f.app.db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// TestAuditCheckNoticesACutOffEnd: rows removed from the end since the last check, which no
// later row could point at, break the chain too.
func TestAuditCheckNoticesACutOffEnd(t *testing.T) {
	f := newAPI(t, false)
	ctx := context.Background()
	for _, code := range []string{"shop", "pay"} {
		if _, err := f.app.access.CreateProduct(ctx, access.Actor{UserID: testAdmin, Origin: access.OriginWeb}, access.NewProduct{Code: code, Name: code}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.app.checkAudit(ctx); err != nil {
		t.Fatal(err)
	}
	tamper(t, f, `DELETE FROM audit_log WHERE seq = (SELECT MAX(seq) FROM audit_log)`)
	if err := f.app.checkAudit(ctx); !errors.Is(err, errAuditBroken) {
		t.Fatalf("check after the last row was removed: %v", err)
	}
}

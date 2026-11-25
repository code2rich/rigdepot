// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package audit_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
	"github.com/code2rich/rigdepot/internal/server/migrations"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

var chainKey = bytes.Repeat([]byte{0x5a}, audit.KeySize)

func migrated(t *testing.T, db *dbx.DB) *audit.Log {
	t.Helper()
	if _, err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	l, err := audit.New(db, chainKey)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

var (
	alice = audit.Actor{UserID: "0193a4b2-6c1e-7a00-8000-000000000011", Origin: audit.OriginCLI, ViaTool: "claude-code", DeviceID: "0193a4b2-6c1e-7a00-8000-000000000201"}
	bob   = audit.Actor{UserID: "0193a4b2-6c1e-7a00-8000-000000000012", Origin: audit.OriginWeb}
)

func publish(by audit.Actor, code string, to int) audit.Entry {
	return audit.Entry{Actor: by, Action: "resource.publish", Object: audit.Object{Kind: "resource", ID: code, Label: "Task " + code},
		Change: map[string]any{"version": map[string]int{"from": to - 1, "to": to}}}
}

func record(t *testing.T, l *audit.Log, entries ...audit.Entry) {
	t.Helper()
	for _, e := range entries {
		if _, err := l.Record(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
}

func verify(t *testing.T, l *audit.Log) audit.Report {
	t.Helper()
	rep, err := l.Verify(context.Background(), 1, "")
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestAppendChainsAndVerifies(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		l := migrated(t, db)
		if rep := verify(t, l); rep.Checked != 0 || rep.Broken != nil {
			t.Errorf("empty log: %+v", rep)
		}
		record(t, l, publish(alice, "task-dev", 2), publish(bob, "unit-tests", 5),
			audit.Entry{Category: audit.CategoryLogin, Actor: audit.Actor{Origin: audit.OriginWeb}, Method: "local", Action: "login.local",
				Object: audit.Object{Kind: "user", ID: "nobody", Label: "nobody"}, Result: audit.ResultDenied, Reason: "invalid credentials"},
			audit.Entry{Actor: audit.System, Action: "settings.update", Object: audit.Object{Kind: "settings", ID: "platform", Label: "Platform settings"}})
		rep := verify(t, l)
		if rep.Broken != nil || rep.Checked != 4 || rep.LastSeq != 4 || rep.LastHash == "" {
			t.Fatalf("report %+v", rep)
		}
		// Checking incrementally from the last report's anchor.
		record(t, l, publish(alice, "task-dev", 3))
		inc, err := l.Verify(context.Background(), rep.LastSeq+1, rep.LastHash)
		if err != nil || inc.Broken != nil || inc.Checked != 1 || inc.LastSeq != 5 {
			t.Errorf("incremental: %+v %v", inc, err)
		}

		page, err := l.Query(context.Background(), audit.Filter{}, 0, "")
		if err != nil || len(page.Rows) != 5 || page.NextCursor != "" {
			t.Fatalf("query: %d rows, cursor %q, %v", len(page.Rows), page.NextCursor, err)
		}
		first := page.Rows[4]
		if first.Seq != 1 || first.Actor != alice || first.Object.Label != "Task task-dev" || first.Result != audit.ResultOK ||
			first.Category != audit.CategoryPlatform || first.Change != `{"version":{"from":1,"to":2}}` {
			t.Errorf("first row %+v", first)
		}
		if login := page.Rows[2]; login.Method != "local" || login.Actor.UserID != "" || login.Reason != "invalid credentials" {
			t.Errorf("login row %+v", login)
		}
	})
}

// TestAuditFailureRollsBackTheChange is the acceptance check "when the audit
// entry cannot be written, the operation is rolled back".
func TestAuditFailureRollsBackTheChange(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		l := migrated(t, db)
		ctx := context.Background()
		change := func(e audit.Entry) error {
			return db.InTx(ctx, func(tx *dbx.Tx) error {
				if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES ('organization.name', '"Example"', 1)`); err != nil {
					return err
				}
				_, err := l.Append(ctx, tx, e)
				return err
			})
		}
		settingsRows := func() int {
			var n int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM settings`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		ok := audit.Entry{Actor: bob, Action: "settings.update", Object: audit.Object{Kind: "settings", ID: "platform", Label: "Platform settings"}}

		// An entry the log refuses.
		bad := ok
		bad.Action = "Update settings"
		if err := change(bad); !errors.Is(err, audit.ErrInvalid) {
			t.Fatalf("invalid entry: %v", err)
		}
		if settingsRows() != 0 {
			t.Error("the change was committed without its audit entry")
		}

		// The database refusing the insert.
		var block, unblock string
		if db.Dialect() == dbx.SQLite {
			block = `CREATE TRIGGER audit_log_full BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT, 'disk full'); END`
			unblock = `DROP TRIGGER audit_log_full`
		} else {
			block = `CREATE FUNCTION audit_log_full() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'disk full'; END; $$;
				CREATE TRIGGER audit_log_full BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION audit_log_full()`
			unblock = `DROP TRIGGER audit_log_full ON audit_log`
		}
		if _, err := db.ExecContext(ctx, block); err != nil {
			t.Fatal(err)
		}
		if err := change(ok); err == nil || !strings.Contains(err.Error(), "disk full") {
			t.Fatalf("insert refused: %v", err)
		}
		if settingsRows() != 0 {
			t.Error("the change was committed without its audit entry")
		}
		db.ExecContext(ctx, unblock)

		if err := change(ok); err != nil {
			t.Fatal(err)
		}
		if settingsRows() != 1 || verify(t, l).Checked != 1 {
			t.Error("the change and its entry were not committed together")
		}
	})
}

// TestUpdateAndDeleteAreRefused is the acceptance check "UPDATE and DELETE
// are refused" by the database itself.
func TestUpdateAndDeleteAreRefused(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		l := migrated(t, db)
		record(t, l, publish(alice, "task-dev", 2), publish(bob, "task-dev", 3))
		ctx := context.Background()
		statements := []string{
			`UPDATE audit_log SET object_label = 'nothing to see' WHERE seq = 1`,
			`UPDATE audit_log SET result = 'ok'`,
			`DELETE FROM audit_log WHERE seq = 2`,
			`DELETE FROM audit_log`,
		}
		if db.Dialect() == dbx.Postgres {
			statements = append(statements, `TRUNCATE audit_log`)
		}
		for _, s := range statements {
			_, err := db.ExecContext(ctx, s)
			if err == nil || !strings.Contains(err.Error(), "append-only") {
				t.Errorf("%s: %v", s, err)
			}
		}
		if rep := verify(t, l); rep.Checked != 2 || rep.Broken != nil {
			t.Errorf("after the refused statements: %+v", rep)
		}
	})
}

// tamper runs statements as someone with full access to the database would:
// with the append-only triggers out of the way.
func tamper(t *testing.T, db *dbx.DB, statements ...string) {
	t.Helper()
	ctx := context.Background()
	off, on := []string{`DROP TRIGGER audit_log_no_update`, `DROP TRIGGER audit_log_no_delete`}, []string(nil)
	if db.Dialect() == dbx.Postgres {
		off, on = []string{`ALTER TABLE audit_log DISABLE TRIGGER USER`}, []string{`ALTER TABLE audit_log ENABLE TRIGGER USER`}
	}
	for _, s := range append(append(off, statements...), on...) {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

// TestChangedRowIsNoticed is the acceptance check "after a row in the
// database is changed, verification fails" - for the ways someone with the
// database but without the chain key could try.
func TestChangedRowIsNoticed(t *testing.T) {
	cases := []struct {
		name     string
		tamper   []string
		brokenAt int64
	}{
		{"label changed", []string{`UPDATE audit_log SET object_label = 'Something else' WHERE seq = 3`}, 3},
		{"denied made ok", []string{`UPDATE audit_log SET result = 'ok', reason = '' WHERE seq = 2`}, 2},
		{"actor removed", []string{`UPDATE audit_log SET actor_id = NULL WHERE seq = 4`}, 4},
		{"time moved", []string{`UPDATE audit_log SET at = at + 1 WHERE seq = 1`}, 1},
		{"row removed", []string{`DELETE FROM audit_log WHERE seq = 3`}, 4},
		{"rows renumbered after a removal", []string{`DELETE FROM audit_log WHERE seq = 3`, `UPDATE audit_log SET seq = seq - 1 WHERE seq > 3`}, 3},
		{"hash recomputed without the key", []string{`UPDATE audit_log SET object_label = 'x', hash = '` + strings.Repeat("ab", 32) + `' WHERE seq = 5`}, 5},
		{"row inserted", []string{`INSERT INTO audit_log (seq, at, category, origin, action, object_kind, object_id, object_label, result, prev_hash, hash)
			SELECT 6, at, category, origin, action, object_kind, object_id, object_label, result, hash, hash FROM audit_log WHERE seq = 5`}, 6},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
				l := migrated(t, db)
				denied := publish(alice, "task-dev", 3)
				denied.Result, denied.Reason = audit.ResultDenied, "not a maintainer"
				record(t, l, publish(alice, "task-dev", 2), denied, publish(bob, "unit-tests", 1), publish(bob, "unit-tests", 2), publish(alice, "task-dev", 3))
				tamper(t, db, c.tamper...)
				rep := verify(t, l)
				if rep.Broken == nil || rep.Broken.Seq != c.brokenAt {
					t.Errorf("report %+v, want broken at %d", rep, c.brokenAt)
				}
			})
		})
	}
}

func TestCutOffEndIsNoticedFromTheLastAnchor(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		l := migrated(t, db)
		record(t, l, publish(alice, "a", 1), publish(alice, "a", 2), publish(alice, "a", 3))
		rep := verify(t, l)
		tamper(t, db, `DELETE FROM audit_log WHERE seq >= 2`)
		next, err := l.Verify(context.Background(), rep.LastSeq+1, rep.LastHash)
		if err != nil || next.Broken == nil {
			t.Errorf("after cutting off the end: %+v %v", next, err)
		}
	})
}

func TestAnotherKeyDoesNotVerify(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		l := migrated(t, db)
		record(t, l, publish(alice, "a", 1))
		other, _ := audit.New(db, bytes.Repeat([]byte{1}, audit.KeySize))
		if rep, _ := other.Verify(context.Background(), 1, ""); rep.Broken == nil || rep.Broken.Seq != 1 {
			t.Errorf("verified with another key: %+v", rep)
		}
		if _, err := audit.New(db, []byte("short")); err == nil {
			t.Error("a short key was accepted")
		}
	})
}

// TestConcurrentAppendsStayConsecutive: entries from many requests at once
// are numbered without gaps or repeats and still form one chain.
func TestConcurrentAppendsStayConsecutive(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		l := migrated(t, db)
		var wg sync.WaitGroup
		errs := make(chan error, 40)
		for i := range 40 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := l.Record(context.Background(), publish(alice, fmt.Sprintf("r-%d", i), 1))
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		if rep := verify(t, l); rep.Broken != nil || rep.Checked != 40 {
			t.Errorf("report %+v", rep)
		}
	})
}

func TestQueryFiltersAndPages(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		l := migrated(t, db)
		clock := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
		l.SetClock(func() time.Time { clock = clock.Add(time.Minute); return clock })
		record(t, l,
			publish(alice, "task-dev", 2), // 09:01
			publish(bob, "task-dev", 3),   // 09:02
			audit.Entry{Actor: bob, Action: "product.member.add", Object: audit.Object{Kind: "user", ID: alice.UserID, Label: "alice"}}, // 09:03
			audit.Entry{Actor: bob, Action: "resource.disable", Object: audit.Object{Kind: "resource", ID: "unit-tests", Label: "Unit tests"}, Result: audit.ResultDenied},
			audit.Entry{Category: audit.CategoryLogin, Actor: bob, Method: "oidc", Action: "login.oidc", Object: audit.Object{Kind: "user", ID: bob.UserID, Label: "bob"}},
		)
		ctx := context.Background()
		seqs := func(f audit.Filter) string {
			t.Helper()
			page, err := l.Query(ctx, f, 0, "")
			if err != nil {
				t.Fatal(err)
			}
			var s []string
			for _, r := range page.Rows {
				s = append(s, fmt.Sprint(r.Seq))
			}
			return strings.Join(s, ",")
		}
		for name, c := range map[string]struct {
			f    audit.Filter
			want string
		}{
			"all":             {audit.Filter{}, "5,4,3,2,1"},
			"actor":           {audit.Filter{ActorID: alice.UserID}, "1"},
			"action":          {audit.Filter{Action: "resource.publish"}, "2,1"},
			"action prefix":   {audit.Filter{Action: "resource."}, "4,2,1"},
			"prefix is exact": {audit.Filter{Action: "resource"}, ""},
			"object":          {audit.Filter{ObjectKind: "resource", ObjectID: "task-dev"}, "2,1"},
			"result":          {audit.Filter{Result: audit.ResultDenied}, "4"},
			"category":        {audit.Filter{Category: audit.CategoryLogin}, "5"},
			"since until":     {audit.Filter{Since: time.Date(2026, 11, 25, 9, 2, 0, 0, time.UTC), Until: time.Date(2026, 11, 25, 9, 4, 0, 0, time.UTC)}, "3,2"},
			"concerning":      {audit.Filter{Concerning: alice.UserID}, "3,1"},
		} {
			if got := seqs(c.f); got != c.want {
				t.Errorf("%s: %s, want %s", name, got, c.want)
			}
		}

		var all []int64
		cursor := ""
		for range 10 {
			page, err := l.Query(ctx, audit.Filter{}, 2, cursor)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range page.Rows {
				all = append(all, r.Seq)
			}
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
			// A new entry while paging does not shift the pages.
			record(t, l, publish(alice, "late", 1))
		}
		if fmt.Sprint(all) != "[5 4 3 2 1]" {
			t.Errorf("pages: %v", all)
		}
		for _, bad := range []string{"x", "eyJzIjowfQ", "bm90IGpzb24"} {
			if _, err := l.Query(ctx, audit.Filter{}, 2, bad); !errors.Is(err, audit.ErrBadCursor) {
				t.Errorf("cursor %q: %v", bad, err)
			}
		}
	})
}

func TestCSVExport(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		l := migrated(t, db)
		e := publish(alice, "task-dev", 2)
		e.Object.Label = `=HYPERLINK("http://example.com","click")`
		record(t, l, e, audit.Entry{Actor: bob, Action: "settings.update", Object: audit.Object{Kind: "settings", ID: "platform", Label: "Platform, settings"},
			Change: map[string]any{"mail.password": "replaced"}})
		rows, err := l.Scan(context.Background(), audit.Filter{})
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var buf bytes.Buffer
		names := audit.Names{User: func(id string) string {
			if id == alice.UserID {
				return "alice"
			}
			return ""
		}}
		if err := audit.WriteCSV(&buf, rows, names); err != nil {
			t.Fatal(err)
		}
		records, err := csv.NewReader(&buf).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		if len(records) != 3 || strings.Join(records[0], ",") != strings.Join(audit.CSVHeader, ",") {
			t.Fatalf("records %q", records)
		}
		one, two := records[1], records[2]
		if one[0] != "1" || one[3] != "alice" || one[5] != "claude-code" || one[10] != `'=HYPERLINK("http://example.com","click")` ||
			one[11] != `{"version":{"from":1,"to":2}}` {
			t.Errorf("first record %q", one)
		}
		if two[3] != bob.UserID || two[10] != "Platform, settings" || two[11] != `{"mail.password":"replaced"}` {
			t.Errorf("second record %q", two)
		}
	})
}

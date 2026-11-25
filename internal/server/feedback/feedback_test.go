// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package feedback_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/catalog"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
	"github.com/code2rich/rigdepot/internal/server/events"
	"github.com/code2rich/rigdepot/internal/server/feedback"
	"github.com/code2rich/rigdepot/internal/server/migrations"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// recorder keeps the events published after their transaction committed.
type recorder struct {
	mu  sync.Mutex
	got []events.Payload
}

func (r *recorder) Publish(tx events.Tx, p events.Payload) {
	tx.AfterCommit(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.got = append(r.got, p)
	})
}

func (r *recorder) all() []events.Payload {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.got)
}

// resources stands in for the catalog: three resources by id.
type resources map[string]catalog.Brief

func (rs resources) BriefByCode(_ context.Context, code string) (catalog.Brief, bool, error) {
	for _, b := range rs {
		if b.Code == code {
			return b, true, nil
		}
	}
	return catalog.Brief{}, false, nil
}

func (rs resources) Briefs(_ context.Context, ids []string) (map[string]catalog.Brief, error) {
	out := map[string]catalog.Brief{}
	for _, id := range ids {
		if b, ok := rs[id]; ok {
			out[id] = b
		}
	}
	return out, nil
}

type accessAuditor struct{ log *audit.Log }

func (a accessAuditor) Append(ctx context.Context, tx *dbx.Tx, e access.AuditEntry) error {
	_, err := a.log.Append(ctx, tx, audit.Entry{Actor: audit.Actor{UserID: e.Actor.UserID, Origin: audit.Origin(e.Actor.Origin)},
		Action: string(e.Action), Object: audit.Object{Kind: e.ObjectKind, ID: e.ObjectID, Label: e.ObjectLabel}, Change: e.Change,
		Result: audit.Result(e.Result), Reason: e.Reason})
	return err
}

// fixture: admin (platform administrator), owner (of shop), member (of shop), maint (maintainer
// of the org resource), outsider (in no product). Resources: shop-skill (shop, v3), org-skill
// (org, v1, maintained by maint), other-skill (product other, which member is not in).
type fixture struct {
	t      *testing.T
	db     *dbx.DB
	log    *audit.Log
	svc    *feedback.Service
	events *recorder
	users  map[string]string
}

func newFixture(t *testing.T, db *dbx.DB) *fixture {
	t.Helper()
	ctx := context.Background()
	if _, err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	log, err := audit.New(db, make([]byte, audit.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	acc := access.New(db, access.Options{Auditor: accessAuditor{log}})
	f := &fixture{t: t, db: db, log: log, events: &recorder{}, users: map[string]string{}}
	for _, c := range []struct {
		name  string
		admin bool
	}{{"admin", true}, {"owner", false}, {"member", false}, {"maint", false}, {"outsider", false}} {
		err := db.InTx(ctx, func(tx *dbx.Tx) error {
			u, err := acc.CreateUser(ctx, tx, access.System, access.NewUser{Username: c.name, DisplayName: "Name of " + c.name,
				Email: c.name + "@example.com", Status: access.StatusActive, Admin: c.admin})
			f.users[c.name] = u.ID
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	shop, err := acc.CreateProduct(ctx, access.System, access.NewProduct{Code: "shop", Name: "Shop", Owners: []string{f.users["owner"]}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := acc.CreateProduct(ctx, access.System, access.NewProduct{Code: "other", Name: "Other", Owners: []string{f.users["admin"]}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := acc.PutMember(ctx, access.System, "shop", f.users["member"], access.RoleMember); err != nil {
		t.Fatal(err)
	}
	rs := resources{
		"r-shop":  {ID: "r-shop", Code: "shop-skill", Kind: "skill", Name: "Shop skill", Version: 3, Product: shop.ID, Target: access.TargetResource(access.InProduct, shop.ID, "", nil)},
		"r-org":   {ID: "r-org", Code: "org-skill", Kind: "skill", Name: "Org skill", Version: 1, Target: access.TargetResource(access.InOrg, "", "", []string{f.users["maint"]})},
		"r-other": {ID: "r-other", Code: "other-skill", Kind: "skill", Name: "Other skill", Version: 1, Product: other.ID, Target: access.TargetResource(access.InProduct, other.ID, "", nil)},
	}
	// feedback.resource_id references resources: the rows have to exist.
	for id, b := range rs {
		if _, err := db.ExecContext(ctx, `INSERT INTO resources (id, code, kind, origin, scope_kind, policy, audience, status, name, created_at, updated_at)
			VALUES (?, ?, 'skill', 'upload', 'org', 'recommended', 'rd', 'published', ?, 1, 1)`, id, b.Code, b.Name); err != nil {
			t.Fatal(err)
		}
	}
	f.svc, err = feedback.New(db, acc, rs, feedback.Options{Auditor: log, Events: f.events})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func newSQLite(t *testing.T) *fixture { return newFixture(t, dbxtest.Open(t, dbxtest.SQLiteURL(t))) }

func (f *fixture) web(name string) access.Actor {
	return access.Actor{UserID: f.users[name], Origin: access.OriginWeb}
}

func (f *fixture) cli(name string) access.Actor {
	return access.Actor{UserID: f.users[name], Origin: access.OriginCLI, DeviceID: "device-1"}
}

func (f *fixture) submit(a access.Actor, n feedback.Submission) feedback.Feedback {
	f.t.Helper()
	fb, err := f.svc.Create(context.Background(), a, n)
	if err != nil {
		f.t.Fatal(err)
	}
	return fb
}

func (f *fixture) update(a access.Actor, number int, c feedback.Change) feedback.Feedback {
	f.t.Helper()
	fb, err := f.svc.Update(context.Background(), a, number, c)
	if err != nil {
		f.t.Fatal(err)
	}
	return fb
}

func (f *fixture) auditCount(action string, result audit.Result) int {
	f.t.Helper()
	page, err := f.log.Query(context.Background(), audit.Filter{Action: action, Result: result}, audit.MaxLimit, "")
	if err != nil {
		f.t.Fatal(err)
	}
	return len(page.Rows)
}

func ptr[T any](v T) *T { return &v }

func bug(resource string) feedback.Submission {
	return feedback.Submission{Type: feedback.TypeBug, Title: "Generated var in a Java 8 project", Body: "The tests did not compile.", Resource: resource}
}

func fieldCodes(err error) []string {
	var fe *feedback.Error
	if !errors.As(err, &fe) {
		return nil
	}
	var out []string
	for _, x := range fe.Fields {
		out = append(out, x.Field+":"+x.Code)
	}
	return out
}

// TestContextKeepsOnlyTheAgreedFields: each way in keeps its own fields; the rest is dropped,
// and nothing outside the agreed fields can be stored at all.
func TestContextKeepsOnlyTheAgreedFields(t *testing.T) {
	f := newSQLite(t)
	ctx := context.Background()
	all := feedback.Context{Page: "/catalog/shop-skill?token=abc#x", Browser: "Firefox 140", Tool: "claude-code", ToolVersion: "2.1.330",
		CLIVersion: "0.5.0", OS: "darwin", LastSync: "partial", Frictions: []feedback.FrictionCount{{Type: "repeated_correction", Count: 2}},
		Trigger: "error rate over 30%"}

	n := bug("shop-skill")
	n.Context = all
	web := f.submit(f.web("member"), n)
	if web.Origin != feedback.OriginWeb || !jsonEqual(web.Context, feedback.Context{Page: "/catalog/shop-skill", Browser: "Firefox 140"}) {
		t.Errorf("from the console: %s %+v", web.Origin, web.Context)
	}
	n.AIDrafted = true
	cli := f.submit(f.cli("member"), n)
	want := all
	want.Page, want.Browser, want.Trigger = "", "", ""
	if cli.Origin != feedback.OriginAI || !jsonEqual(cli.Context, want) {
		t.Errorf("from an AI tool: %s %+v", cli.Origin, cli.Context)
	}
	// What is stored is exactly that: the agreed keys, nothing else.
	var stored string
	if err := f.db.QueryRowContext(ctx, `SELECT context FROM feedback WHERE number = ?`, web.Number).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != `{"page":"/catalog/shop-skill","browser":"Firefox 140"}` {
		t.Errorf("stored context: %s", stored)
	}

	n = bug("")
	n.Context = feedback.Context{Tool: "Claude Code", LastSync: "great", Frictions: []feedback.FrictionCount{{Type: "chat_log", Count: 1}, {Type: "tool_error", Count: 0}}}
	_, err := f.svc.Create(ctx, f.cli("member"), n)
	got := fieldCodes(err)
	for _, w := range []string{"context.tool:pattern", "context.last_sync:enum", "context.frictions[0].type:enum", "context.frictions[1].count:range"} {
		if !slices.Contains(got, w) {
			t.Errorf("missing %s in %v", w, got)
		}
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func TestCreateChecksTheResource(t *testing.T) {
	f := newSQLite(t)
	ctx := context.Background()
	for _, c := range []struct {
		n    feedback.Submission
		want string
	}{
		{bug("no-such-skill"), "resource:unknown"},
		{bug("other-skill"), "resource:unknown"}, // member cannot see it
		{feedback.Submission{Type: feedback.TypeBug, Title: "x", Body: "y", Resource: "shop-skill", ResourceVersion: 4}, "resource_version:unknown"},
		{feedback.Submission{Type: feedback.TypeBug, Title: "x", Body: "y", ResourceVersion: 1}, "resource_version:without_resource"},
		{feedback.Submission{Type: feedback.TypeFriction, Title: "x", Body: "y"}, "type:enum"},
		{feedback.Submission{Type: feedback.TypeBug, Title: strings.Repeat("长", 81), Body: "y"}, "title:too_long"},
		{feedback.Submission{Type: feedback.TypeBug, Title: " ", Body: "y"}, "title:required"},
	} {
		_, err := f.svc.Create(ctx, f.web("member"), c.n)
		if got := fieldCodes(err); !slices.Contains(got, c.want) {
			t.Errorf("%+v: %v, want %s", c.n, err, c.want)
		}
	}
	fb := f.submit(f.web("member"), feedback.Submission{Type: feedback.TypeQuestion, Title: "x", Body: "y", Resource: "shop-skill", ResourceVersion: 3})
	if fb.Number != 1 || fb.Status != feedback.StatusNew || fb.Severity != feedback.SeverityMedium || fb.Resource == nil || fb.Resource.Code != "shop-skill" ||
		len(fb.Events) != 1 || fb.Events[0].Kind != feedback.EventCreated {
		t.Fatalf("created: %+v", fb)
	}
	if f.auditCount("feedback.create", audit.ResultOK) != 1 {
		t.Error("the submission is not in the audit log")
	}
	page, _ := f.log.Query(ctx, audit.Filter{Action: "feedback."}, audit.MaxLimit, "")
	for _, r := range page.Rows {
		if b, _ := json.Marshal(r); strings.Contains(string(b), fb.Title) && fb.Title != "x" {
			t.Errorf("the audit log holds the title: %s", b)
		}
	}
}

func TestWhoSeesAndHandles(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		f := newFixture(t, db)
		ctx := context.Background()
		shop := f.submit(f.web("member"), bug("shop-skill"))
		org := f.submit(f.web("member"), bug("org-skill"))
		none := f.submit(f.web("outsider"), bug(""))

		sees := map[string][]int{
			"member":   {shop.Number, org.Number},
			"owner":    {shop.Number},
			"maint":    {org.Number},
			"outsider": {none.Number},
			"admin":    {shop.Number, org.Number, none.Number},
		}
		for who, want := range sees {
			list, next, err := f.svc.List(ctx, f.web(who), feedback.Filter{}, 0, "")
			if err != nil || next != "" {
				t.Fatal(err)
			}
			var got []int
			for _, fb := range list {
				got = append(got, fb.Number)
			}
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Errorf("%s sees %v, want %v", who, got, want)
			}
			for _, n := range []int{shop.Number, org.Number, none.Number} {
				_, err := f.svc.Get(ctx, f.web(who), n)
				if slices.Contains(want, n) != (err == nil) {
					t.Errorf("%s gets FB-%d: %v", who, n, err)
				}
			}
		}

		// The submitter cannot handle; the refusal is in the audit log.
		_, err := f.svc.Update(ctx, f.web("member"), shop.Number, feedback.Change{Status: ptr(feedback.StatusResolved)})
		if !errors.Is(err, feedback.ErrDenied) {
			t.Errorf("submitter resolves: %v", err)
		}
		_, err = f.svc.Update(ctx, f.web("outsider"), shop.Number, feedback.Change{Status: ptr(feedback.StatusResolved)})
		if !errors.Is(err, feedback.ErrNotFound) {
			t.Errorf("outsider resolves: %v", err)
		}
		if n := f.auditCount("feedback.manage", audit.ResultDenied); n != 2 {
			t.Errorf("%d refusals in the audit log, want 2", n)
		}
		// Assigning is only to someone who can handle it.
		_, err = f.svc.Update(ctx, f.web("owner"), shop.Number, feedback.Change{Assignee: ptr(f.users["outsider"])})
		if got := fieldCodes(err); !slices.Equal(got, []string{"assignee:not_handler"}) {
			t.Errorf("assign to an outsider: %v", err)
		}
		fb := f.update(f.web("admin"), shop.Number, feedback.Change{Assignee: ptr(f.users["owner"]), Severity: ptr(feedback.SeverityHigh)})
		if fb.AssigneeID != f.users["owner"] || fb.Severity != feedback.SeverityHigh {
			t.Errorf("assign: %+v", fb)
		}
		// Anyone who sees it may reply; the reply is published without its text.
		if _, err := f.svc.Comment(ctx, f.web("outsider"), shop.Number, "hello"); !errors.Is(err, feedback.ErrNotFound) {
			t.Errorf("outsider replies: %v", err)
		}
		ev, err := f.svc.Comment(ctx, f.web("member"), shop.Number, "Still happens on v3.")
		if err != nil || ev.Kind != feedback.EventComment || ev.Body != "Still happens on v3." || ev.ActorID != f.users["member"] {
			t.Fatalf("reply: %v %+v", err, ev)
		}
		var commented []events.FeedbackCommented
		for _, p := range f.events.all() {
			if c, ok := p.(events.FeedbackCommented); ok {
				commented = append(commented, c)
			}
		}
		if len(commented) != 1 || commented[0].Number != shop.Number || commented[0].AssigneeID != f.users["owner"] || commented[0].ProductID == "" {
			t.Errorf("comment events: %+v", commented)
		}
	})
}

// TestWontFixNeedsAReason: declining without a reason is refused; the reason is kept.
func TestWontFixNeedsAReason(t *testing.T) {
	f := newSQLite(t)
	ctx := context.Background()
	fb := f.submit(f.web("member"), bug("shop-skill"))
	for _, reason := range []*string{nil, ptr(""), ptr("   ")} {
		_, err := f.svc.Update(ctx, f.web("owner"), fb.Number, feedback.Change{Status: ptr(feedback.StatusWontFix), StatusReason: reason})
		if !errors.Is(err, feedback.ErrReasonRequired) {
			t.Errorf("wont_fix with reason %v: %v", reason, err)
		}
	}
	if got, _ := f.svc.Get(ctx, f.web("owner"), fb.Number); got.Status != feedback.StatusNew {
		t.Fatalf("a refused change changed the status: %s", got.Status)
	}
	got := f.update(f.web("owner"), fb.Number, feedback.Change{Status: ptr(feedback.StatusWontFix), StatusReason: ptr("Java 8 is out of support.")})
	last := got.Events[len(got.Events)-1]
	if got.Status != feedback.StatusWontFix || got.StatusReason != "Java 8 is out of support." || last.Kind != feedback.EventStatus || last.Body != got.StatusReason {
		t.Errorf("declined: %+v", got)
	}
	// Back to new is not a status a handler sets.
	_, err := f.svc.Update(ctx, f.web("owner"), fb.Number, feedback.Change{Status: ptr(feedback.StatusNew)})
	if gotCodes := fieldCodes(err); !slices.Equal(gotCodes, []string{"status:enum"}) {
		t.Errorf("back to new: %v", err)
	}
}

// TestLowScoreReopens: a score of 1 or 2 reopens resolved feedback and tells its handler; a
// higher one does not.
func TestLowScoreReopens(t *testing.T) {
	f := newSQLite(t)
	ctx := context.Background()
	fb := f.submit(f.web("member"), bug("shop-skill"))

	if _, err := f.svc.Rate(ctx, f.web("member"), fb.Number, 5, ""); !errors.Is(err, feedback.ErrNotResolved) {
		t.Errorf("rate before it is resolved: %v", err)
	}
	resolved := f.update(f.web("owner"), fb.Number, feedback.Change{Status: ptr(feedback.StatusResolved)})
	if resolved.AssigneeID != f.users["owner"] {
		t.Errorf("resolving unassigned feedback did not assign the handler: %+v", resolved)
	}
	if _, err := f.svc.Rate(ctx, f.web("owner"), fb.Number, 5, ""); !errors.Is(err, feedback.ErrDenied) {
		t.Errorf("the handler rates: %v", err)
	}
	if _, err := f.svc.Rate(ctx, f.web("member"), fb.Number, 6, ""); fieldCodes(err) == nil {
		t.Errorf("score 6: %v", err)
	}

	got, err := f.svc.Rate(ctx, f.web("member"), fb.Number, 2, "Same error on the next run")
	if err != nil {
		t.Fatal(err)
	}
	n := len(got.Events)
	if got.Status != feedback.StatusAccepted || got.Score != 2 || got.ScoreNote != "Same error on the next run" ||
		got.Events[n-2].Kind != feedback.EventRated || got.Events[n-1].Kind != feedback.EventReopened {
		t.Fatalf("after a low score: %+v", got)
	}
	var changes []events.FeedbackStatusChanged
	for _, p := range f.events.all() {
		if c, ok := p.(events.FeedbackStatusChanged); ok {
			changes = append(changes, c)
		}
	}
	reopen := changes[len(changes)-1]
	if reopen.Old != events.FeedbackResolved || reopen.New != events.FeedbackAccepted || reopen.ActorID != f.users["member"] ||
		reopen.AssigneeID != f.users["owner"] || reopen.SubmitterID != f.users["member"] {
		t.Errorf("reopen event: %+v", reopen)
	}
	if first := changes[0]; first.Old != events.FeedbackNew || first.New != events.FeedbackResolved {
		t.Errorf("resolve event: %+v", first)
	}

	// Resolved again: the old score is gone; a good score leaves it resolved.
	again := f.update(f.web("owner"), fb.Number, feedback.Change{Status: ptr(feedback.StatusResolved)})
	if again.Score != 0 {
		t.Errorf("a new resolution kept the old score: %+v", again)
	}
	final, err := f.svc.Rate(ctx, f.web("member"), fb.Number, 3, "")
	if err != nil || final.Status != feedback.StatusResolved || final.Score != 3 {
		t.Errorf("score 3: %v %+v", err, final)
	}
	if s, _ := f.svc.Summary(ctx, f.web("member"), true); s.Open != 0 || s.ByStatus[feedback.StatusResolved] != 1 {
		t.Errorf("summary: %+v", s)
	}
}

func TestListFiltersAndPages(t *testing.T) {
	f := newSQLite(t)
	ctx := context.Background()
	var numbers []int
	for i := 0; i < 5; i++ {
		numbers = append(numbers, f.submit(f.web("member"), bug("shop-skill")).Number)
	}
	f.submit(f.web("member"), feedback.Submission{Type: feedback.TypeSuggestion, Title: "x", Body: "y"})
	f.update(f.web("owner"), numbers[0], feedback.Change{Status: ptr(feedback.StatusInProgress)})

	var seen []int
	cursor := ""
	for {
		page, next, err := f.svc.List(ctx, f.web("member"), feedback.Filter{Resource: "shop-skill"}, 2, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, fb := range page {
			seen = append(seen, fb.Number)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	// Most recently updated first: the one in progress, then newest to oldest.
	if want := []int{numbers[0], numbers[4], numbers[3], numbers[2], numbers[1]}; !slices.Equal(seen, want) {
		t.Errorf("pages: %v, want %v", seen, want)
	}
	if list, _, _ := f.svc.List(ctx, f.web("owner"), feedback.Filter{AssignedToMe: true}, 0, ""); len(list) != 1 || list[0].Number != numbers[0] {
		t.Errorf("assigned to the owner: %+v", list)
	}
	if list, _, _ := f.svc.List(ctx, f.web("member"), feedback.Filter{Type: feedback.TypeSuggestion}, 0, ""); len(list) != 1 {
		t.Errorf("suggestions: %+v", list)
	}
	if list, _, _ := f.svc.List(ctx, f.web("member"), feedback.Filter{Resource: "no-such"}, 0, ""); len(list) != 0 {
		t.Errorf("unknown resource: %+v", list)
	}
	if _, _, err := f.svc.List(ctx, f.web("member"), feedback.Filter{}, 0, "%%%"); fieldCodes(err) == nil {
		t.Errorf("bad cursor: %v", err)
	}
	s, err := f.svc.Summary(ctx, f.web("owner"), false)
	if err != nil || s.Open != 5 || s.ByStatus[feedback.StatusInProgress] != 1 || s.ByStatus[feedback.StatusNew] != 4 {
		t.Errorf("owner's summary: %v %+v", err, s)
	}
}

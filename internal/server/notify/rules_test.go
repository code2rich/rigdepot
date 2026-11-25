// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package notify

import (
	"context"
	"errors"
	"maps"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
	"github.com/code2rich/rigdepot/internal/server/events"
)

// The people of the rule tests. Products: shop (owned by owner), pay (owned by owner2) and
// orphan (no owner yet).
const (
	uAdmin     = "0193a4b2-6c1e-7a00-8000-0000000000a1"
	uOldAdmin  = "0193a4b2-6c1e-7a00-8000-0000000000a2" // disabled
	uOwner     = "0193a4b2-6c1e-7a00-8000-0000000000b1"
	uOwner2    = "0193a4b2-6c1e-7a00-8000-0000000000b2"
	uMember    = "0193a4b2-6c1e-7a00-8000-0000000000c1"
	uMaint     = "0193a4b2-6c1e-7a00-8000-0000000000c2" // maintains resources, has no e-mail address
	uApplicant = "0193a4b2-6c1e-7a00-8000-0000000000d1" // pending
	pShop      = "0193a4b2-6c1e-7a00-8000-0000000000e1"
	pPay       = "0193a4b2-6c1e-7a00-8000-0000000000e2"
	pOrphan    = "0193a4b2-6c1e-7a00-8000-0000000000e3"
)

// directory is a Directory with fixed people.
type directory struct {
	users    []access.User
	products []access.Product
	err      error
}

func (d *directory) ListUsers(_ context.Context, a access.Actor, _ access.UserFilter) ([]access.User, error) {
	if !a.IsSystem() {
		return nil, errors.New("asked as someone other than the system")
	}
	return d.users, d.err
}

func (d *directory) ListProducts(_ context.Context, a access.Actor) ([]access.Product, error) {
	if !a.IsSystem() {
		return nil, errors.New("asked as someone other than the system")
	}
	return d.products, d.err
}

func testDirectory() *directory {
	shop := access.ProductRef{ID: pShop, Code: "shop", Name: "Shop"}
	pay := access.ProductRef{ID: pPay, Code: "pay", Name: "Payments"}
	user := func(id, name, email, locale string, status access.Status, admin bool, stages []string, ms ...access.Membership) access.User {
		return access.User{ID: id, Username: strings.ToLower(name), DisplayName: name, Email: email, Locale: locale, Status: status,
			Admin: admin, FocusStages: stages, Memberships: ms}
	}
	return &directory{
		users: []access.User{
			user(uAdmin, "Admin", "admin@example.com", "en", access.StatusActive, true, nil),
			user(uOldAdmin, "Old admin", "old@example.com", "en", access.StatusDisabled, true, nil),
			user(uOwner, "Owner", "owner@example.com", "zh-CN", access.StatusActive, false, []string{"develop"},
				access.Membership{Product: shop, Role: access.RoleOwner}),
			user(uOwner2, "Owner two", "owner2@example.com", "", access.StatusActive, false, []string{"test"},
				access.Membership{Product: pay, Role: access.RoleOwner}),
			user(uMember, "Member", "member@example.com", "en", access.StatusActive, false, []string{"develop"},
				access.Membership{Product: shop, Role: access.RoleMember}),
			user(uMaint, "Maintainer", "", "en", access.StatusActive, false, []string{"develop"},
				access.Membership{Product: shop, Role: access.RoleMember}),
			user(uApplicant, "Applicant", "applicant@example.com", "en", access.StatusPending, false, nil),
		},
		products: []access.Product{
			{ID: pShop, Code: "shop", Name: "Shop"},
			{ID: pPay, Code: "pay", Name: "Payments"},
			{ID: pOrphan, Code: "orphan", Name: "Orphan"},
		},
	}
}

// delivered is what one event caused: the template each user got in the app, and the template
// each address got by mail.
type delivered struct {
	inApp map[string]string // user id → template
	mail  map[string]string // address → template
}

func (f *fixture) delivered() delivered {
	f.t.Helper()
	ctx := context.Background()
	d := delivered{inApp: map[string]string{}, mail: map[string]string{}}
	rows, err := f.db.QueryContext(ctx, `SELECT user_id, message_key FROM notifications`)
	if err != nil {
		f.t.Fatal(err)
	}
	for rows.Next() {
		var u, k string
		if err := rows.Scan(&u, &k); err != nil {
			f.t.Fatal(err)
		}
		d.inApp[u] = k
	}
	rows.Close()
	rows, err = f.db.QueryContext(ctx, `SELECT recipient, template FROM mail_outbox`)
	if err != nil {
		f.t.Fatal(err)
	}
	for rows.Next() {
		var a, k string
		if err := rows.Scan(&a, &k); err != nil {
			f.t.Fatal(err)
		}
		d.mail[a] = k
	}
	rows.Close()
	return d
}

func newRulesFixture(t *testing.T, db *dbx.DB) *fixture {
	t.Helper()
	dir := testDirectory()
	f := newFixture(t, db, func(o *Options) {
		o.People = dir
		o.BaseURL, _ = url.Parse("https://depot.example.com/hub")
	})
	// The accounts the notifications refer to.
	for _, u := range dir.users {
		if _, err := db.ExecContext(context.Background(), `INSERT INTO users (id, username, display_name, status, is_admin, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 1, 1)`, u.ID, u.Username, u.DisplayName, string(u.Status), u.Admin); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// handle delivers one event through the built-in rules.
func (f *fixture) handle(p events.Payload) error {
	rule, ok := Rules()[p.EventName()]
	if !ok {
		f.t.Fatalf("no rule for %s", p.EventName())
	}
	return f.svc.Handle(context.Background(), events.Event{ID: dbx.NewID(), Name: p.EventName(), At: f.now, Payload: p}, rule)
}

// TestRulesReachTheRightPeople: every kind of event notifies the right people, in the app and
// by mail as its rule says, and never whoever caused it or a disabled account.
func TestRulesReachTheRightPeople(t *testing.T) {
	shopRes := events.ResourceRef{ResourceID: "r1", Code: "unit-testing", Kind: "skill", Name: "Unit testing", Scope: "shop", ProductID: pShop,
		Stages: []string{"test"}, Maintainers: []string{uMaint}}
	orgRes := events.ResourceRef{ResourceID: "r2", Code: "review", Kind: "skill", Name: "Review", Scope: "org",
		Stages: []string{"develop"}, Maintainers: []string{uMaint}}
	fb := events.FeedbackRef{FeedbackID: "f1", Number: 42, SubmitterID: uMember, AssigneeID: uOwner, ResourceCode: "unit-testing", ProductID: pShop}
	none := map[string]string{}
	cases := []struct {
		name  string
		event events.Payload
		inApp map[string]string
		mail  map[string]string
	}{
		{"join request to the product's owners", access.JoinRequested{RequestID: "j1", UserID: uApplicant, ProductID: pShop},
			map[string]string{uOwner: TemplateJoinRequested}, map[string]string{"owner@example.com": TemplateJoinRequested}},
		{"join request to a product without owners: the administrators", access.JoinRequested{RequestID: "j2", UserID: uApplicant, ProductID: pOrphan},
			map[string]string{uAdmin: TemplateJoinRequested}, map[string]string{"admin@example.com": TemplateJoinRequested}},
		{"join request rejected", access.JoinDecided{RequestID: "j1", UserID: uApplicant, ProductID: pShop, Reason: "ask your lead first"},
			map[string]string{uApplicant: TemplateJoinRejected}, map[string]string{"applicant@example.com": TemplateJoinRejected}},
		{"join request approved: by mail, the membership is in the app", access.JoinDecided{RequestID: "j1", UserID: uApplicant, ProductID: pShop, Approved: true},
			none, map[string]string{"applicant@example.com": TemplateJoinApproved}},
		{"added to a product", access.MembershipChanged{ProductID: pShop, UserID: uMember, New: access.RoleMember},
			map[string]string{uMember: TemplateMemberAdded}, none},
		{"made an owner", access.MembershipChanged{ProductID: pShop, UserID: uMember, Old: access.RoleMember, New: access.RoleOwner},
			map[string]string{uMember: TemplateOwnerAdded}, none},
		{"no longer an owner", access.MembershipChanged{ProductID: pShop, UserID: uOwner, Old: access.RoleOwner, New: access.RoleMember},
			map[string]string{uOwner: TemplateOwnerRemoved}, none},
		{"removed from a product", access.MembershipChanged{ProductID: pShop, UserID: uMember, Old: access.RoleMember},
			map[string]string{uMember: TemplateMemberRemoved}, none},
		{"a disabled account hears nothing", access.MembershipChanged{ProductID: pShop, UserID: uOldAdmin, Old: access.RoleMember},
			none, none},
		{"made an administrator", access.AdminChanged{UserID: uMember, Admin: true},
			map[string]string{uMember: TemplateAdminGranted}, none},
		{"audit chain broken: the active administrators", events.AuditChainBroken{Seq: 9, IntactThrough: 8, Problem: "hash mismatch"},
			map[string]string{uAdmin: TemplateAuditBroken}, map[string]string{"admin@example.com": TemplateAuditBroken}},
		{"draft of a product's resource: maintainers and owners, not who uploaded it", events.ResourceDraftUpdated{ResourceRef: shopRes, ActorID: uMaint},
			map[string]string{uOwner: TemplateResourceDraft}, none},
		{"new organisation resource imported: maintainers and administrators", events.ResourceDraftUpdated{ResourceRef: orgRes, Created: true},
			map[string]string{uMaint: TemplateResourceNewDraft, uAdmin: TemplateResourceNewDraft}, none},
		{"published in a product: maintainers and the product's owners, not the publisher", events.ResourcePublished{ResourceRef: shopRes, Version: 5, Previous: 4, ActorID: uOwner},
			map[string]string{uMaint: TemplateResourcePublished}, none},
		{"published for the organisation: maintainers and owners who focus on its stage", events.ResourcePublished{ResourceRef: orgRes, Version: 2, Previous: 1, ActorID: uAdmin},
			map[string]string{uMaint: TemplateResourcePublished, uOwner: TemplateResourcePublished}, none},
		{"rolled back", events.ResourcePublished{ResourceRef: shopRes, Version: 6, Previous: 5, RollbackOf: 4, ActorID: uMaint},
			map[string]string{uOwner: TemplateResourceRolledBack}, none},
		{"feedback resolved: the submitter", events.FeedbackStatusChanged{FeedbackRef: fb, Old: events.FeedbackInProgress, New: events.FeedbackResolved, ActorID: uOwner},
			map[string]string{uMember: TemplateFeedbackResolved}, map[string]string{"member@example.com": TemplateFeedbackResolved}},
		{"feedback reopened by a low score: the assignee", events.FeedbackStatusChanged{FeedbackRef: fb, Old: events.FeedbackResolved, New: events.FeedbackAccepted, ActorID: uMember},
			map[string]string{uOwner: TemplateFeedbackReopened}, map[string]string{"owner@example.com": TemplateFeedbackReopened}},
		{"feedback accepted by the system: submitter and assignee", events.FeedbackStatusChanged{FeedbackRef: fb, Old: events.FeedbackNew, New: events.FeedbackAccepted},
			map[string]string{uMember: TemplateFeedbackAccepted, uOwner: TemplateFeedbackAccepted},
			map[string]string{"member@example.com": TemplateFeedbackAccepted, "owner@example.com": TemplateFeedbackAccepted}},
		{"reply on feedback: the other side, in the app", events.FeedbackCommented{FeedbackRef: fb, ActorID: uMember},
			map[string]string{uOwner: TemplateFeedbackCommented}, none},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newRulesFixture(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)))
			if err := f.handle(c.event); err != nil {
				t.Fatal(err)
			}
			got := f.delivered()
			if !maps.Equal(got.inApp, c.inApp) {
				t.Errorf("in the app: %v, want %v", got.inApp, c.inApp)
			}
			if !maps.Equal(got.mail, c.mail) {
				t.Errorf("by mail: %v, want %v", got.mail, c.mail)
			}
		})
	}
}

// TestEveryRuleHasACase: a rule added to Rules gets a row in the table above.
func TestEveryRuleHasACase(t *testing.T) {
	want := []string{"access.admin_changed", "access.join_decided", "access.join_requested", "access.membership_changed",
		"audit.chain_broken", "catalog.draft_updated", "catalog.published", "feedback.commented", "feedback.status_changed"}
	if got := slices.Sorted(maps.Keys(Rules())); !slices.Equal(got, want) {
		t.Errorf("rules for %v; the table covers %v", got, want)
	}
}

// TestFailingMailKeepsTheInAppNotification: neither a mail server that refuses nor an outbox
// that cannot be written to costs anyone their in-app notification.
func TestFailingMailKeepsTheInAppNotification(t *testing.T) {
	ctx := context.Background()
	ev := events.AuditChainBroken{Seq: 3, IntactThrough: 2}

	f := newRulesFixture(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)))
	f.ch.setFail(-1)
	if err := f.handle(ev); err != nil {
		t.Fatal(err)
	}
	f.run()
	if n, err := f.svc.Unread(ctx, uAdmin); err != nil || n != 1 {
		t.Errorf("unread after the mail failed: %d %v", n, err)
	}
	if st := f.stats(); st.Retrying != 1 {
		t.Errorf("the mail is not waiting for a retry: %+v", st)
	}

	f = newRulesFixture(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)))
	if _, err := f.db.ExecContext(ctx, `DROP TABLE mail_outbox`); err != nil {
		t.Fatal(err)
	}
	if err := f.handle(ev); err == nil {
		t.Error("queuing into a missing outbox did not fail")
	}
	if n, err := f.svc.Unread(ctx, uAdmin); err != nil || n != 1 {
		t.Errorf("unread when the mail could not be queued: %d %v", n, err)
	}
}

// TestUnknownPeopleIsAnError: when the directory cannot be read, the event fails (and is logged
// by the bus) rather than reaching nobody silently.
func TestUnknownPeopleIsAnError(t *testing.T) {
	f := newFixture(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)), func(o *Options) {
		d := testDirectory()
		d.err = errors.New("database is locked")
		o.People = d
	})
	if err := f.handle(events.AuditChainBroken{Seq: 3}); err == nil {
		t.Error("no error")
	}
}

// TestNotificationsInTheReadersLanguage: the in-app notification is rendered when it is read,
// in the language asked for; the mail in the recipient's, with the link under the public
// address.
func TestNotificationsInTheReadersLanguage(t *testing.T) {
	ctx := context.Background()
	f := newRulesFixture(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)))
	if err := f.handle(access.JoinRequested{RequestID: "j1", UserID: uApplicant, ProductID: pShop}); err != nil {
		t.Fatal(err)
	}
	en, err := f.svc.List(ctx, uOwner, "en", ListOptions{})
	if err != nil || len(en.Items) != 1 {
		t.Fatalf("list: %+v %v", en, err)
	}
	zh, _ := f.svc.List(ctx, uOwner, "zh-CN", ListOptions{})
	if en.Items[0].Title != "Applicant asked to join Shop" || zh.Items[0].Title != "Applicant 申请加入 Shop" ||
		!strings.Contains(zh.Items[0].Body, "产品与成员") || en.Items[0].Link != "/products" || en.Items[0].Kind != "access.join_requested" {
		t.Errorf("en %+v\nzh %+v", en.Items[0], zh.Items[0])
	}
	f.run()
	msgs := f.ch.messages()
	if len(msgs) != 1 || msgs[0].Subject != "Applicant 申请加入 Shop" || msgs[0].Locale != "zh-CN" || msgs[0].To.Name != "Owner" ||
		!strings.Contains(msgs[0].Text, "https://depot.example.com/hub/products") {
		t.Errorf("mail: %+v", msgs)
	}
}

// TestInbox: pages newest first, the unread count over all pages, marking read only one's own.
func TestInbox(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		ctx := context.Background()
		f := newRulesFixture(t, db)
		for i := range 5 {
			f.now = f.now.Add(time.Second)
			if err := f.handle(events.FeedbackCommented{FeedbackRef: events.FeedbackRef{FeedbackID: "f", Number: i + 1, SubmitterID: uMember}, ActorID: uOwner}); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.handle(events.FeedbackCommented{FeedbackRef: events.FeedbackRef{FeedbackID: "g", Number: 99, SubmitterID: uOwner}, ActorID: uMember}); err != nil {
			t.Fatal(err)
		}
		var titles []string
		var ids []string
		cursor := ""
		for {
			p, err := f.svc.List(ctx, uMember, "en", ListOptions{Limit: 2, Cursor: cursor})
			if err != nil {
				t.Fatal(err)
			}
			if p.Unread != 5 {
				t.Errorf("unread %d, want 5", p.Unread)
			}
			for _, it := range p.Items {
				titles = append(titles, it.Title)
				ids = append(ids, it.ID)
			}
			if cursor = p.NextCursor; cursor == "" {
				break
			}
		}
		want := []string{"New reply on FB-5", "New reply on FB-4", "New reply on FB-3", "New reply on FB-2", "New reply on FB-1"}
		if !slices.Equal(titles, want) {
			t.Errorf("titles %v", titles)
		}
		other, _ := f.svc.List(ctx, uOwner, "en", ListOptions{})
		// Marking someone else's notification does nothing.
		if n, err := f.svc.MarkRead(ctx, uMember, []string{ids[0], ids[1], other.Items[0].ID, "not-an-id"}); err != nil || n != 2 {
			t.Errorf("MarkRead: %d %v", n, err)
		}
		if n, _ := f.svc.Unread(ctx, uOwner); n != 1 {
			t.Errorf("the owner's notification was marked: unread %d", n)
		}
		unread, _ := f.svc.List(ctx, uMember, "en", ListOptions{UnreadOnly: true})
		if len(unread.Items) != 3 || unread.Unread != 3 || unread.Items[0].ReadAt != nil {
			t.Errorf("unread only: %+v", unread)
		}
		all, _ := f.svc.List(ctx, uMember, "en", ListOptions{})
		if all.Items[0].ReadAt == nil || all.Items[2].ReadAt != nil {
			t.Errorf("read_at: %+v", all.Items)
		}
		if n, err := f.svc.MarkAllRead(ctx, uMember); err != nil || n != 3 {
			t.Errorf("MarkAllRead: %d %v", n, err)
		}
		if n, _ := f.svc.Unread(ctx, uMember); n != 0 {
			t.Errorf("unread after all read: %d", n)
		}
		if n, _ := f.svc.Unread(ctx, uOwner); n != 1 {
			t.Errorf("marking all read reached someone else: %d", n)
		}
		if _, err := f.svc.List(ctx, uMember, "en", ListOptions{Cursor: "%%%"}); !errors.Is(err, ErrBadCursor) {
			t.Errorf("bad cursor: %v", err)
		}
		if _, err := f.svc.List(ctx, uMember, "en", ListOptions{Limit: MaxPageSize + 1}); !errors.Is(err, ErrBadLimit) {
			t.Errorf("limit too large: %v", err)
		}
		if _, err := f.svc.MarkRead(ctx, uMember, make([]string, MaxMarkRead+1)); !errors.Is(err, ErrTooMany) {
			t.Errorf("too many ids: %v", err)
		}
	})
}

// TestMailInvite: without mail the administrator gets the link (ErrMailOff); with it the
// invitation is mailed in the server's language, and the outbox forgets the link once sent.
func TestMailInvite(t *testing.T) {
	ctx := context.Background()
	f := newRulesFixture(t, dbxtest.Open(t, dbxtest.SQLiteURL(t)))
	const link = "https://depot.example.com/hub/invite/<占位：令牌>"
	inv := access.Invite{ID: "i1", Email: "new@example.com", CreatedBy: access.UserRef{DisplayName: "Admin"},
		CreatedAt: f.now, ExpiresAt: f.now.Add(access.InviteValidity)}
	f.cfg.mu.Lock()
	f.cfg.configured = false
	f.cfg.mu.Unlock()
	if err := f.svc.MailInvite(ctx, inv, link); !errors.Is(err, ErrMailOff) {
		t.Errorf("without mail: %v", err)
	}
	if st := f.stats(); st.Queued != 0 {
		t.Errorf("queued without mail: %+v", st)
	}
	f.cfg.mu.Lock()
	f.cfg.configured = true
	f.cfg.mu.Unlock()
	if err := f.svc.MailInvite(ctx, inv, link); err != nil {
		t.Fatal(err)
	}
	f.run()
	msgs := f.ch.messages()
	if len(msgs) != 1 || msgs[0].To.Address != "new@example.com" || msgs[0].Link != link ||
		!strings.Contains(msgs[0].Text, "Admin invited you") || !strings.Contains(msgs[0].Text, "within 7 days") {
		t.Fatalf("sent %+v", msgs)
	}
	var stored int
	if err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mail_outbox WHERE link LIKE '%invite%' OR params LIKE '%invite/%'`).Scan(&stored); err != nil || stored != 0 {
		t.Errorf("the outbox still holds the invitation link: %d %v", stored, err)
	}
}

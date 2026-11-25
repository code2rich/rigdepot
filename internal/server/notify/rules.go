// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package notify

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/events"
)

// Notice is one notification a rule decides on: to one person, in the app, by mail or both.
type Notice struct {
	// To is the recipient's user id. Disabled accounts and unknown ids get nothing.
	To string
	// Address is an e-mail address for someone without an account (an invitation); it is
	// only mailed. Set To or Address, not both.
	Address string
	// Template names the message (templates); Params fill its placeholders. Params are
	// identifiers and short labels, never secrets: they are stored and rendered later.
	Template string
	Params   map[string]string
	// Link is where to see the details: a path in the console, such as /products. Mail gets
	// it as an absolute address.
	Link string
	// InApp writes an in-app notification; Mail queues a mail when the recipient has an
	// address.
	InApp, Mail bool
}

// Rule decides which notices an event causes. It runs on the bus's subscriber goroutine after
// the change was committed, with everyone who might hear of it.
type Rule func(ctx context.Context, ev events.Event, people *People) ([]Notice, error)

// Directory is where notify finds people and products: package access, asked as the system.
// *access.Service implements it.
type Directory interface {
	ListUsers(ctx context.Context, a access.Actor, f access.UserFilter) ([]access.User, error)
	ListProducts(ctx context.Context, a access.Actor) ([]access.Product, error)
}

var _ Directory = (*access.Service)(nil)

// People are the accounts and products at the time an event is handled.
type People struct {
	users    []access.User
	byID     map[string]int
	products map[string]access.Product
}

func loadPeople(ctx context.Context, d Directory) (*People, error) {
	users, err := d.ListUsers(ctx, access.System, access.UserFilter{})
	if err != nil {
		return nil, err
	}
	products, err := d.ListProducts(ctx, access.System)
	if err != nil {
		return nil, err
	}
	p := &People{users: users, byID: make(map[string]int, len(users)), products: make(map[string]access.Product, len(products))}
	for i, u := range users {
		p.byID[u.ID] = i
	}
	for _, pr := range products {
		p.products[pr.ID] = pr
	}
	return p, nil
}

// User returns the account with the id.
func (p *People) User(id string) (access.User, bool) {
	i, ok := p.byID[id]
	if !ok {
		return access.User{}, false
	}
	return p.users[i], true
}

// Product returns the product with the id.
func (p *People) Product(id string) (access.Product, bool) {
	pr, ok := p.products[id]
	return pr, ok
}

// Admins returns the active platform administrators.
func (p *People) Admins() []string {
	var out []string
	for _, u := range p.users {
		if u.Admin && u.Status == access.StatusActive {
			out = append(out, u.ID)
		}
	}
	return out
}

// Owners returns the owners of a product.
func (p *People) Owners(productID string) []string {
	var out []string
	for _, u := range p.users {
		for _, m := range u.Memberships {
			if m.Product.ID == productID && m.Role == access.RoleOwner {
				out = append(out, u.ID)
			}
		}
	}
	return out
}

// OwnersFocusing returns the owners of any product who focus on one of the stages
// (resource-catalog: notifications go to the owners who focus on a resource's stage).
func (p *People) OwnersFocusing(stages []string) []string {
	var out []string
	for _, u := range p.users {
		owner := slices.ContainsFunc(u.Memberships, func(m access.Membership) bool { return m.Role == access.RoleOwner })
		if owner && slices.ContainsFunc(u.FocusStages, func(s string) bool { return slices.Contains(stages, s) }) {
			out = append(out, u.ID)
		}
	}
	return out
}

// recipients merges lists of user ids, without duplicates and without the ones in except (who
// caused the event: nobody is told of what they did themselves).
func recipients(except []string, lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		for _, id := range l {
			if id != "" && !slices.Contains(except, id) && !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	return out
}

// to makes one notice per recipient from a template.
func to(ids []string, n Notice) []Notice {
	out := make([]Notice, 0, len(ids))
	for _, id := range ids {
		c := n
		c.To = id
		out = append(out, c)
	}
	return out
}

// On delivers the notices rule returns for each event with one of the given names. The in-app
// notifications are written in one transaction and the mail queued in another, so that mail
// that cannot be queued never costs anyone their in-app notification; sending happens later
// in the outbox job, whose failures touch neither. A rule that fails is logged by the bus; the
// change that caused the event stands.
func (s *Service) On(bus *events.Bus, names []string, rule Rule) (unsubscribe func()) {
	return bus.Subscribe("notify", names, func(ctx context.Context, ev events.Event) error {
		return s.Handle(ctx, ev, rule)
	})
}

// Handle runs rule on one event and delivers its notices. On calls it for every event; tests
// call it directly.
func (s *Service) Handle(ctx context.Context, ev events.Event, rule Rule) error {
	people := &People{}
	if s.people != nil {
		var err error
		if people, err = loadPeople(ctx, s.people); err != nil {
			return err
		}
	}
	notices, err := rule(ctx, ev, people)
	if err != nil || len(notices) == 0 {
		return err
	}
	at := ev.At
	if at.IsZero() {
		at = s.now()
	}
	var inApp []Notice
	var mails []Mail
	for _, n := range notices {
		if _, ok := templates[n.Template]; !ok {
			return errors.New("notify: no template " + strconv.Quote(n.Template))
		}
		if n.To == "" {
			if n.Mail && n.Address != "" {
				mails = append(mails, Mail{To: n.Address, Template: n.Template, Params: n.Params, Link: s.absolute(n.Link)})
			}
			continue
		}
		u, ok := people.User(n.To)
		if !ok || u.Status == access.StatusDisabled {
			continue
		}
		if n.InApp {
			inApp = append(inApp, n)
		}
		if n.Mail && u.Email != "" {
			mails = append(mails, Mail{To: u.Email, Name: u.DisplayName, Locale: u.Locale, Template: n.Template, Params: n.Params, Link: s.absolute(n.Link)})
		}
	}
	var errs []error
	if len(inApp) > 0 {
		errs = append(errs, s.db.InTx(ctx, func(tx *dbx.Tx) error {
			for _, n := range inApp {
				if err := insertNotification(ctx, tx, at, n.To, ev.Name, n.Template, n.Params, n.Link); err != nil {
					return err
				}
			}
			return nil
		}))
	}
	if len(mails) > 0 {
		errs = append(errs, s.db.InTx(ctx, func(tx *dbx.Tx) error { return s.Enqueue(ctx, tx, mails...) }))
	}
	return errors.Join(errs...)
}

// absolute turns a console path into the address mail links to.
func (s *Service) absolute(path string) string {
	if path == "" || s.baseURL == nil {
		return path
	}
	return s.baseURL.JoinPath(path).String()
}

// Subscribe registers the built-in rules (Rules) on bus.
func (s *Service) Subscribe(bus *events.Bus) (unsubscribe func()) {
	rules := Rules()
	names := make([]string, 0, len(rules))
	for name := range rules {
		names = append(names, name)
	}
	slices.Sort(names)
	return s.On(bus, names, func(ctx context.Context, ev events.Event, people *People) ([]Notice, error) {
		return rules[ev.Name](ctx, ev, people)
	})
}

// Rules are the built-in rules by event name: who hears of what, and how.
//
//	event                       to                                              in-app  mail
//	access.join_requested       the product's owners; none: the administrators  yes     yes
//	access.join_decided         the person who asked                            reject  yes
//	access.membership_changed   the member                                      yes     no
//	access.admin_changed        the person                                      yes     no
//	audit.chain_broken          the administrators                              yes     yes
//	catalog.draft_updated       maintainers; the product's owners, or the       yes     no
//	                            administrators for the whole organisation
//	catalog.published           maintainers; the product's owners, or the       yes     no
//	                            owners who focus on one of its stages
//	feedback.status_changed     submitter and assignee                          yes     yes
//	feedback.commented          submitter and assignee                          yes     no
//
// Whoever caused an event is not told of it. An approved join request is told by mail only: the
// membership it creates is the in-app notification. Invitations are mailed when they are made
// (MailInvite), since their link is not in the event.
func Rules() map[string]Rule {
	return map[string]Rule{
		access.JoinRequested{}.EventName():         joinRequested,
		access.JoinDecided{}.EventName():           joinDecided,
		access.MembershipChanged{}.EventName():     membershipChanged,
		access.AdminChanged{}.EventName():          adminChanged,
		events.AuditChainBroken{}.EventName():      auditBroken,
		events.ResourceDraftUpdated{}.EventName():  draftUpdated,
		events.ResourcePublished{}.EventName():     published,
		events.FeedbackStatusChanged{}.EventName(): feedbackStatus,
		events.FeedbackCommented{}.EventName():     feedbackCommented,
	}
}

func productName(people *People, id string) string {
	if p, ok := people.Product(id); ok {
		return p.Name
	}
	return id
}

func joinRequested(_ context.Context, ev events.Event, people *People) ([]Notice, error) {
	e, ok := ev.Payload.(access.JoinRequested)
	if !ok {
		return nil, nil
	}
	name := e.UserID
	if u, ok := people.User(e.UserID); ok {
		name = u.DisplayName
	}
	n := Notice{Template: TemplateJoinRequested, Params: map[string]string{"user": name, "product_name": productName(people, e.ProductID)},
		Link: "/products", InApp: true, Mail: true}
	if owners := recipients([]string{e.UserID}, people.Owners(e.ProductID)); len(owners) > 0 {
		return to(owners, n), nil
	}
	// A product without owners: the administrators decide.
	n.Link = "/admin/users"
	return to(recipients([]string{e.UserID}, people.Admins()), n), nil
}

func joinDecided(_ context.Context, ev events.Event, people *People) ([]Notice, error) {
	e, ok := ev.Payload.(access.JoinDecided)
	if !ok {
		return nil, nil
	}
	product := productName(people, e.ProductID)
	if e.Approved {
		return []Notice{{To: e.UserID, Template: TemplateJoinApproved, Params: map[string]string{"product_name": product}, Link: "/", Mail: true}}, nil
	}
	return []Notice{{To: e.UserID, Template: TemplateJoinRejected, Params: map[string]string{"product_name": product, "reason": e.Reason},
		Link: "/apply", InApp: true, Mail: true}}, nil
}

func membershipChanged(_ context.Context, ev events.Event, people *People) ([]Notice, error) {
	e, ok := ev.Payload.(access.MembershipChanged)
	if !ok {
		return nil, nil
	}
	n := Notice{To: e.UserID, Params: map[string]string{"product_name": productName(people, e.ProductID)}, Link: "/catalog", InApp: true}
	switch {
	case e.New == "":
		n.Template = TemplateMemberRemoved
	case e.New == access.RoleOwner:
		n.Template, n.Link = TemplateOwnerAdded, "/products"
	case e.Old == access.RoleOwner:
		n.Template = TemplateOwnerRemoved
	case e.Old == "":
		n.Template = TemplateMemberAdded
	default:
		return nil, nil
	}
	return []Notice{n}, nil
}

func adminChanged(_ context.Context, ev events.Event, _ *People) ([]Notice, error) {
	e, ok := ev.Payload.(access.AdminChanged)
	if !ok {
		return nil, nil
	}
	n := Notice{To: e.UserID, Template: TemplateAdminRevoked, Link: "/", InApp: true}
	if e.Admin {
		n.Template, n.Link = TemplateAdminGranted, "/admin/settings"
	}
	return []Notice{n}, nil
}

func auditBroken(_ context.Context, ev events.Event, people *People) ([]Notice, error) {
	e, ok := ev.Payload.(events.AuditChainBroken)
	if !ok {
		return nil, nil
	}
	n := Notice{Template: TemplateAuditBroken, Link: "/admin/audit", InApp: true, Mail: true,
		Params: map[string]string{"seq": strconv.FormatInt(e.Seq, 10), "intact": strconv.FormatInt(e.IntactThrough, 10)}}
	return to(people.Admins(), n), nil
}

// reviewers are who decide on a resource's drafts: its maintainers, and the owners of its
// product or, for the whole organisation, the administrators. A resource whose product does not
// exist (yet) is the administrators' too.
func reviewers(r events.ResourceRef, people *People) []string {
	if r.ProductID != "" {
		if owners := people.Owners(r.ProductID); len(owners) > 0 {
			return append(slices.Clone(r.Maintainers), owners...)
		}
	}
	if r.Scope == "me" {
		return r.Maintainers
	}
	return append(slices.Clone(r.Maintainers), people.Admins()...)
}

func resourceLabel(r events.ResourceRef) string {
	if r.Name != "" {
		return r.Name
	}
	return r.Code
}

func draftUpdated(_ context.Context, ev events.Event, people *People) ([]Notice, error) {
	e, ok := ev.Payload.(events.ResourceDraftUpdated)
	if !ok {
		return nil, nil
	}
	n := Notice{Template: TemplateResourceDraft, Params: map[string]string{"resource": resourceLabel(e.ResourceRef)},
		Link: "/manage/resources/" + e.Code, InApp: true}
	if e.Created {
		n.Template = TemplateResourceNewDraft
	}
	return to(recipients([]string{e.ActorID}, reviewers(e.ResourceRef, people)), n), nil
}

func published(_ context.Context, ev events.Event, people *People) ([]Notice, error) {
	e, ok := ev.Payload.(events.ResourcePublished)
	if !ok {
		return nil, nil
	}
	n := Notice{Template: TemplateResourcePublished, Link: "/manage/resources/" + e.Code, InApp: true,
		Params: map[string]string{"resource": resourceLabel(e.ResourceRef), "version": strconv.Itoa(e.Version)}}
	if e.RollbackOf > 0 {
		n.Template = TemplateResourceRolledBack
		n.Params["rollback_of"] = strconv.Itoa(e.RollbackOf)
	}
	var owners []string
	switch {
	case e.ProductID != "":
		owners = people.Owners(e.ProductID)
	case e.Scope != "me":
		owners = people.OwnersFocusing(e.Stages)
	}
	return to(recipients([]string{e.ActorID}, e.Maintainers, owners), n), nil
}

// feedbackPeople are the submitter and the assignee, without whoever acted.
func feedbackPeople(f events.FeedbackRef, actor string) []string {
	return recipients([]string{actor}, []string{f.SubmitterID, f.AssigneeID})
}

func feedbackStatus(_ context.Context, ev events.Event, _ *People) ([]Notice, error) {
	e, ok := ev.Payload.(events.FeedbackStatusChanged)
	if !ok {
		return nil, nil
	}
	closed := func(s string) bool { return s == events.FeedbackResolved || s == events.FeedbackWontFix }
	var template string
	switch {
	case closed(e.Old) && !closed(e.New):
		template = TemplateFeedbackReopened
	case e.New == events.FeedbackAccepted:
		template = TemplateFeedbackAccepted
	case e.New == events.FeedbackInProgress:
		template = TemplateFeedbackInProgress
	case e.New == events.FeedbackResolved:
		template = TemplateFeedbackResolved
	case e.New == events.FeedbackWontFix:
		template = TemplateFeedbackWontFix
	default:
		return nil, nil
	}
	n := Notice{Template: template, Params: map[string]string{"number": strconv.Itoa(e.Number)},
		Link: "/feedback/" + strconv.Itoa(e.Number), InApp: true, Mail: true}
	return to(feedbackPeople(e.FeedbackRef, e.ActorID), n), nil
}

func feedbackCommented(_ context.Context, ev events.Event, _ *People) ([]Notice, error) {
	e, ok := ev.Payload.(events.FeedbackCommented)
	if !ok {
		return nil, nil
	}
	n := Notice{Template: TemplateFeedbackCommented, Params: map[string]string{"number": strconv.Itoa(e.Number)},
		Link: "/feedback/" + strconv.Itoa(e.Number), InApp: true}
	return to(feedbackPeople(e.FeedbackRef, e.ActorID), n), nil
}

// ErrMailOff is MailInvite's answer while mail is not set up: the caller shows the link instead.
var ErrMailOff = errors.New("notify: mail is not set up")

// MailInvite queues the invitation mail with its link, in the server's default language (the
// invitee has no account yet). While mail is not set up it returns ErrMailOff and queues
// nothing, so that the administrator gets the link to pass on. The link is stored only until
// the mail is sent.
func (s *Service) MailInvite(ctx context.Context, inv access.Invite, link string) error {
	if _, _, _, ok, err := s.channel(ctx); err != nil {
		return err
	} else if !ok {
		return ErrMailOff
	}
	if !oneAddress(inv.Email) {
		return errors.New("notify: the invitation's address is not one e-mail address")
	}
	days := int(access.InviteValidity / (24 * time.Hour))
	if !inv.ExpiresAt.IsZero() && !inv.CreatedAt.IsZero() {
		days = int(inv.ExpiresAt.Sub(inv.CreatedAt).Round(time.Hour) / (24 * time.Hour))
	}
	inviter := inv.CreatedBy.DisplayName
	if inviter == "" {
		inviter = inv.CreatedBy.Username
	}
	return s.db.InTx(ctx, func(tx *dbx.Tx) error {
		return s.Enqueue(ctx, tx, Mail{To: inv.Email, Template: TemplateInvite, Link: link,
			Params: map[string]string{"inviter": inviter, "days": strconv.Itoa(days)}})
	})
}

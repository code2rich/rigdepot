// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package notify

import (
	"fmt"
	"html"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
)

// Template is one kind of message: the message keys of its subject and body. The same template
// is a mail's subject and text and an in-app notification's title and body. The parameters of a
// notice fill their {{placeholders}}.
type Template struct {
	Subject string
	Body    string
	// SecretLink: the link grants something by itself (an invitation), so the outbox forgets it
	// once the mail is sent.
	SecretLink bool
}

// TemplateTest is the message an administrator sends to check the mail settings.
const TemplateTest = "test"

// The templates of the notification rules (rules.go).
const (
	TemplateJoinRequested      = "join_requested"
	TemplateJoinApproved       = "join_approved"
	TemplateJoinRejected       = "join_rejected"
	TemplateMemberAdded        = "member_added"
	TemplateMemberRemoved      = "member_removed"
	TemplateOwnerAdded         = "owner_added"
	TemplateOwnerRemoved       = "owner_removed"
	TemplateAdminGranted       = "admin_granted"
	TemplateAdminRevoked       = "admin_revoked"
	TemplateInvite             = "invite"
	TemplateAuditBroken        = "audit_broken"
	TemplateResourceDraft      = "resource_draft"
	TemplateResourceNewDraft   = "resource_new_draft"
	TemplateResourcePublished  = "resource_published"
	TemplateResourceRolledBack = "resource_rolled_back"
	TemplateFeedbackAccepted   = "feedback_accepted"
	TemplateFeedbackInProgress = "feedback_in_progress"
	TemplateFeedbackResolved   = "feedback_resolved"
	TemplateFeedbackWontFix    = "feedback_wont_fix"
	TemplateFeedbackReopened   = "feedback_reopened"
	TemplateFeedbackCommented  = "feedback_commented"
)

// templates are the kinds of message. Each rule adds its templates here.
var templates = map[string]Template{
	TemplateTest:               {Subject: msg.MailTestSubject, Body: msg.MailTestBody},
	TemplateJoinRequested:      {Subject: msg.NotifyJoinRequestedTitle, Body: msg.NotifyJoinRequestedBody},
	TemplateJoinApproved:       {Subject: msg.NotifyJoinApprovedTitle, Body: msg.NotifyJoinApprovedBody},
	TemplateJoinRejected:       {Subject: msg.NotifyJoinRejectedTitle, Body: msg.NotifyJoinRejectedBody},
	TemplateMemberAdded:        {Subject: msg.NotifyMemberAddedTitle, Body: msg.NotifyMemberAddedBody},
	TemplateMemberRemoved:      {Subject: msg.NotifyMemberRemovedTitle, Body: msg.NotifyMemberRemovedBody},
	TemplateOwnerAdded:         {Subject: msg.NotifyOwnerAddedTitle, Body: msg.NotifyOwnerAddedBody},
	TemplateOwnerRemoved:       {Subject: msg.NotifyOwnerRemovedTitle, Body: msg.NotifyOwnerRemovedBody},
	TemplateAdminGranted:       {Subject: msg.NotifyAdminGrantedTitle, Body: msg.NotifyAdminGrantedBody},
	TemplateAdminRevoked:       {Subject: msg.NotifyAdminRevokedTitle, Body: msg.NotifyAdminRevokedBody},
	TemplateInvite:             {Subject: msg.NotifyInviteTitle, Body: msg.NotifyInviteBody, SecretLink: true},
	TemplateAuditBroken:        {Subject: msg.NotifyAuditBrokenTitle, Body: msg.NotifyAuditBrokenBody},
	TemplateResourceDraft:      {Subject: msg.NotifyResourceDraftTitle, Body: msg.NotifyResourceDraftBody},
	TemplateResourceNewDraft:   {Subject: msg.NotifyResourceNewDraftTitle, Body: msg.NotifyResourceNewDraftBody},
	TemplateResourcePublished:  {Subject: msg.NotifyResourcePublishedTitle, Body: msg.NotifyResourcePublishedBody},
	TemplateResourceRolledBack: {Subject: msg.NotifyResourceRolledBackTitle, Body: msg.NotifyResourceRolledBackBody},
	TemplateFeedbackAccepted:   {Subject: msg.NotifyFeedbackAcceptedTitle, Body: msg.NotifyFeedbackAcceptedBody},
	TemplateFeedbackInProgress: {Subject: msg.NotifyFeedbackInProgressTitle, Body: msg.NotifyFeedbackInProgressBody},
	TemplateFeedbackResolved:   {Subject: msg.NotifyFeedbackResolvedTitle, Body: msg.NotifyFeedbackResolvedBody},
	TemplateFeedbackWontFix:    {Subject: msg.NotifyFeedbackWontFixTitle, Body: msg.NotifyFeedbackWontFixBody},
	TemplateFeedbackReopened:   {Subject: msg.NotifyFeedbackReopenedTitle, Body: msg.NotifyFeedbackReopenedBody},
	TemplateFeedbackCommented:  {Subject: msg.NotifyFeedbackCommentedTitle, Body: msg.NotifyFeedbackCommentedBody},
}

// rendered is a mail in one language.
type rendered struct {
	subject, text, html string
}

// renderer turns templates into text in the recipient's language.
type renderer struct {
	cat           *i18n.Catalog
	defaultLocale string
}

func (r renderer) printer(locale string) *i18n.Printer {
	return r.cat.Printer(r.cat.Match(locale, r.defaultLocale))
}

func args(params map[string]string) []i18n.Arg {
	out := make([]i18n.Arg, 0, len(params))
	for _, k := range slices.Sorted(maps.Keys(params)) {
		out = append(out, i18n.V(k, params[k]))
	}
	return out
}

// one renders a single mail.
func (r renderer) one(locale, template string, params map[string]string, link string) (rendered, error) {
	t, ok := templates[template]
	if !ok {
		return rendered{}, fmt.Errorf("notify: no mail template %q", template)
	}
	p := r.printer(locale)
	a := args(params)
	subject := p.T(t.Subject, a...)
	body := p.T(t.Body, a...)
	return r.finish(p, subject, body, link, nil), nil
}

// text renders a template's subject and body without the mail's layout, for an in-app
// notification. An unknown template (one a newer version wrote, or one since removed) renders as
// its name, so that the notification still shows.
func (r renderer) text(locale, template string, params map[string]string) (title, body string) {
	t, ok := templates[template]
	if !ok {
		return template, ""
	}
	p := r.printer(locale)
	a := args(params)
	return p.T(t.Subject, a...), p.T(t.Body, a...)
}

// digestItem is one queued mail inside a digest.
type digestItem struct {
	subject, link string
}

// digest renders several mails as one, in the language of the first.
func (r renderer) digest(locale string, limit int, items []digestItem) rendered {
	p := r.printer(locale)
	subject := p.T(msg.MailDigestSubject, i18n.Count(len(items)))
	intro := p.T(msg.MailDigestIntro, i18n.V("limit", strconv.Itoa(limit)))
	return r.finish(p, subject, intro, "", items)
}

// finish lays out the plain text and the HTML: the body, the link, the items of a digest and
// the footer. Everything from parameters is escaped in the HTML.
func (r renderer) finish(p *i18n.Printer, subject, body, link string, items []digestItem) rendered {
	footer := p.T(msg.MailFooter)
	var t, h strings.Builder
	t.WriteString(body + "\n")
	h.WriteString(`<!doctype html><html><body style="font:14px/1.6 -apple-system,'PingFang SC','Microsoft YaHei',sans-serif;color:#14161a">`)
	h.WriteString(`<p style="white-space:pre-wrap">` + html.EscapeString(body) + `</p>`)
	if link != "" {
		t.WriteString("\n" + p.T(msg.MailLink, i18n.V("link", link)) + "\n")
		h.WriteString(`<p><a href="` + html.EscapeString(link) + `">` + html.EscapeString(p.T(msg.MailLink, i18n.V("link", link))) + `</a></p>`)
	}
	if len(items) > 0 {
		h.WriteString("<ol>")
		for _, it := range items {
			t.WriteString("\n• " + it.subject + "\n")
			h.WriteString("<li>" + html.EscapeString(it.subject))
			if it.link != "" {
				t.WriteString("  " + it.link + "\n")
				h.WriteString(` · <a href="` + html.EscapeString(it.link) + `">` + html.EscapeString(it.link) + `</a>`)
			}
			h.WriteString("</li>")
		}
		h.WriteString("</ol>")
	}
	t.WriteString("\n-- \n" + footer + "\n")
	h.WriteString(`<p style="color:#666;font-size:12px">` + html.EscapeString(footer) + `</p></body></html>`)
	return rendered{subject: subject, text: t.String(), html: h.String()}
}

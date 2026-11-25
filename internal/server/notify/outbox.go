// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package notify

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	extnotify "github.com/code2rich/rigdepot/ext/notify"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/logging"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/jobs"
)

// EmailChannel is the channel the outbox sends through.
const EmailChannel = "email"

// JobName is the background job that sends the outbox.
const JobName = "mail-outbox"

// The states of an outbox row.
const (
	statusQueued = "queued" // waiting, including between retries
	statusSent   = "sent"
	statusFailed = "failed" // the retry window passed; RetryFailed queues it again
)

// Defaults of Options.
const (
	DefaultHourlyLimit = 30
	DefaultRetryFor    = 24 * time.Hour
	jobEvery           = 30 * time.Second
	jobTimeout         = 5 * time.Minute
	sendTimeout        = 2 * time.Minute // one attempt
	batchSize          = 200             // rows looked at per run
	keepSent           = 30 * 24 * time.Hour
	maxErrorLen        = 500
)

// Mail is one message to queue.
type Mail struct {
	To       string            // the recipient's e-mail address
	Name     string            // the recipient's display name; may be empty
	Locale   string            // the recipient's language; empty means the server's default
	Template string            // a name in templates
	Params   map[string]string // fill the template's placeholders; identifiers and labels, never secrets
	Link     string            // where to see the details, absolute; may be empty
}

// ConfigSource gives the saved settings of a channel.
type ConfigSource interface {
	// ChannelConfig returns the channel's settings as a JSON object. ok is false when the channel
	// is not configured or is turned off. An error means unknown: nothing is sent and nothing is
	// marked failed.
	ChannelConfig(ctx context.Context, channel string) (cfg []byte, ok bool, err error)
}

// Options configure a Service.
type Options struct {
	DB *dbx.DB
	// Jobs runs the outbox. Nil means the caller calls RunOnce itself.
	Jobs *jobs.Scheduler
	// Config gives the channel settings. Nil means mail is not configured.
	Config ConfigSource
	// Channels finds a channel by ID; nil means the registry (ext/notify.Lookup).
	Channels func(id string) (extnotify.Channel, bool)
	// Catalog holds the "server" message domain the templates are in.
	Catalog *i18n.Catalog
	// DefaultLocale is used for recipients without a language.
	DefaultLocale string
	// Sender is the display name mail comes from; default the product's name.
	Sender string
	// HourlyLimit is how many messages one recipient gets per hour at most; the rest are merged
	// into a digest. 0 means DefaultHourlyLimit; negative means no limit.
	HourlyLimit int
	// RetryFor is how long after the first attempt a mail is retried. 0 means DefaultRetryFor.
	RetryFor time.Duration
	Log      *slog.Logger
	// Now is the clock; nil means time.Now. Tests move it.
	Now func() time.Time
	// People finds the recipients of the rules (Subscribe, On). Nil: the rules see nobody, and
	// only notices to e-mail addresses are delivered.
	People Directory
	// BaseURL is the server's public address; mail links to the console under it. Nil leaves
	// the links as paths.
	BaseURL *url.URL
}

// Service is the outbox and the dispatch to channels.
type Service struct {
	db       *dbx.DB
	jobs     *jobs.Scheduler
	config   ConfigSource
	channels func(string) (extnotify.Channel, bool)
	render   renderer
	sender   string
	limit    int
	retryFor time.Duration
	log      *slog.Logger
	now      func() time.Time
	people   Directory
	baseURL  *url.URL

	mu sync.Mutex // one RunOnce at a time in this process
}

// New returns the service and, when Options.Jobs is set, schedules the outbox job.
func New(o Options) (*Service, error) {
	if o.DB == nil || o.Catalog == nil {
		return nil, errors.New("notify: DB and Catalog are required")
	}
	s := &Service{
		db: o.DB, jobs: o.Jobs, config: o.Config, channels: o.Channels,
		render: renderer{cat: o.Catalog, defaultLocale: o.DefaultLocale},
		sender: o.Sender, limit: o.HourlyLimit, retryFor: o.RetryFor, log: o.Log, now: o.Now,
		people: o.People, baseURL: o.BaseURL,
	}
	if s.channels == nil {
		s.channels = extnotify.Lookup
	}
	if s.sender == "" {
		s.sender = brand.Name
	}
	switch {
	case s.limit == 0:
		s.limit = DefaultHourlyLimit
	case s.limit < 0:
		s.limit = 0
	}
	if s.retryFor <= 0 {
		s.retryFor = DefaultRetryFor
	}
	if s.log == nil {
		s.log = logging.Discard()
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.jobs != nil {
		if err := s.jobs.Add(jobs.Job{Name: JobName, Every: jobEvery, Timeout: jobTimeout, Run: s.RunOnce}); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Enqueue queues mails in tx. A mail to an address that is not one e-mail address is skipped
// and logged, so that one bad address does not hold up the others. Sending starts once tx has
// committed.
func (s *Service) Enqueue(ctx context.Context, tx *dbx.Tx, mails ...Mail) error {
	now := dbx.Millis(s.now())
	queued := 0
	for _, m := range mails {
		if _, ok := templates[m.Template]; !ok {
			return fmt.Errorf("notify: no mail template %q", m.Template)
		}
		if !oneAddress(m.To) {
			s.log.Warn("mail not queued: the recipient is not one e-mail address", "template", m.Template)
			continue
		}
		params, err := json.Marshal(m.Params)
		if err != nil {
			return err
		}
		if m.Params == nil {
			params = []byte("{}")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO mail_outbox
			(id, recipient, recipient_name, template, locale, params, link, status, attempts, next_attempt_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
			dbx.NewID(), m.To, m.Name, m.Template, m.Locale, string(params), m.Link, statusQueued, now, now); err != nil {
			return fmt.Errorf("notify: queue a mail: %w", err)
		}
		queued++
	}
	if queued > 0 && s.jobs != nil {
		tx.AfterCommit(func() { s.jobs.Trigger(JobName) })
	}
	return nil
}

func oneAddress(s string) bool {
	if s == "" || strings.ContainsAny(s, "\r\n<>,;\" ") {
		return false
	}
	a, err := mail.ParseAddress(s)
	return err == nil && a.Name == "" && a.Address == s
}

// channel returns the e-mail channel with its settings, or ok false when mail is not
// configured.
func (s *Service) channel(ctx context.Context) (extnotify.Channel, extnotify.Config, []string, bool, error) {
	if s.config == nil {
		return nil, nil, nil, false, nil
	}
	raw, ok, err := s.config.ChannelConfig(ctx, EmailChannel)
	if err != nil || !ok {
		return nil, nil, nil, false, err
	}
	ch, found := s.channels(EmailChannel)
	if !found {
		return nil, nil, nil, false, errors.New("notify: the e-mail channel is not in this build")
	}
	cfg, err := extnotify.ParseConfig(raw)
	if err != nil {
		return nil, nil, nil, false, fmt.Errorf("notify: the e-mail settings cannot be read: %w", err)
	}
	fields, err := extnotify.SecretFields(ch.ConfigSchema())
	if err != nil {
		return nil, nil, nil, false, fmt.Errorf("notify: the e-mail channel's schema cannot be read: %w", err)
	}
	var secrets []string
	for _, f := range fields {
		if v := cfg.String(f); v != "" {
			secrets = append(secrets, v)
		}
	}
	return ch, cfg, secrets, true, nil
}

// redact masks the channel's secret settings, in clear and in base64, and whatever else looks
// like a credential, so that the text can be logged and stored.
func redact(text string, secrets []string) string {
	for _, secret := range secrets {
		for _, form := range []string{base64.StdEncoding.EncodeToString([]byte(secret)), base64.RawStdEncoding.EncodeToString([]byte(secret)), secret} {
			text = strings.ReplaceAll(text, form, "***")
		}
	}
	return logging.Redact(text)
}

// row is one queued mail.
type row struct {
	id, recipient, name, template, locale, link string
	params                                      map[string]string
	attempts                                    int
	firstAttempt                                sql.NullInt64
}

// RunOnce sends what is due. While mail is not configured it does nothing.
func (s *Service) RunOnce(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, cfg, secrets, ok, err := s.channel(ctx)
	if err != nil || !ok {
		return err
	}
	now := s.now()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM mail_outbox WHERE status = ? AND sent_at < ?`,
		statusSent, dbx.Millis(now.Add(-keepSent))); err != nil {
		return err
	}
	due, err := s.due(ctx, now)
	if err != nil {
		return err
	}
	// By recipient, in the order the first mail of each was queued.
	var order []string
	byRecipient := map[string][]row{}
	for _, r := range due {
		if _, seen := byRecipient[r.recipient]; !seen {
			order = append(order, r.recipient)
		}
		byRecipient[r.recipient] = append(byRecipient[r.recipient], r)
	}
	for _, rcpt := range order {
		if err := ctx.Err(); err != nil {
			return err
		}
		rows := byRecipient[rcpt]
		single, digest := rows, []row(nil)
		if s.limit > 0 {
			sent, err := s.sentSince(ctx, rcpt, now.Add(-time.Hour))
			if err != nil {
				return err
			}
			budget := s.limit - sent
			if budget <= 0 {
				continue // until the oldest of this hour is an hour old
			}
			if len(rows) > budget {
				single, digest = rows[:budget-1], rows[budget-1:]
			}
		}
		for _, r := range single {
			out, err := s.render.one(r.locale, r.template, r.params, r.link)
			if err != nil {
				s.markFailed(ctx, []row{r}, err.Error())
				continue
			}
			s.deliver(ctx, ch, cfg, secrets, []row{r}, out, r.template)
		}
		if len(digest) > 0 {
			items := make([]digestItem, 0, len(digest))
			for _, r := range digest {
				out, err := s.render.one(r.locale, r.template, r.params, r.link)
				if err != nil {
					out.subject = r.template
				}
				items = append(items, digestItem{subject: out.subject, link: r.link})
			}
			s.deliver(ctx, ch, cfg, secrets, digest, s.render.digest(digest[0].locale, s.limit, items), "digest")
		}
	}
	return nil
}

func (s *Service) due(ctx context.Context, now time.Time) ([]row, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, recipient, recipient_name, template, locale, params, link, attempts, first_attempt_at
		FROM mail_outbox WHERE status = ? AND next_attempt_at <= ? ORDER BY created_at, id LIMIT ?`,
		statusQueued, dbx.Millis(now), batchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		var r row
		var params string
		if err := rows.Scan(&r.id, &r.recipient, &r.name, &r.template, &r.locale, &params, &r.link, &r.attempts, &r.firstAttempt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(params), &r.params); err != nil {
			return nil, fmt.Errorf("notify: the parameters of mail %s: %w", r.id, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// sentSince counts the messages (a digest is one) sent to a recipient since t.
func (s *Service) sentSince(ctx context.Context, recipient string, t time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT batch) FROM mail_outbox WHERE recipient = ? AND status = ? AND sent_at > ?`,
		recipient, statusSent, dbx.Millis(t)).Scan(&n)
	return n, err
}

// deliver sends one message for rows and records the outcome.
func (s *Service) deliver(ctx context.Context, ch extnotify.Channel, cfg extnotify.Config, secrets []string, rows []row, out rendered, kind string) {
	first := rows[0]
	m := extnotify.Message{
		To:      extnotify.Recipient{Address: first.recipient, Name: first.name},
		From:    s.sender,
		Subject: out.subject,
		Text:    out.text,
		HTML:    out.html,
		Link:    first.link,
		Kind:    kind,
		Locale:  s.render.printer(first.locale).Lang().String(),
	}
	if len(rows) > 1 {
		m.Link = ""
	}
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	err := ch.Send(sendCtx, cfg, m)
	cancel()
	if err != nil {
		reason := clip(redact(err.Error(), secrets), maxErrorLen)
		s.log.Warn("mail not sent; will retry", "kind", kind, "messages", len(rows), "attempt", first.attempts+1, "err", reason)
		if err := s.markRetry(ctx, rows, reason); err != nil {
			s.log.Error("mail: cannot record the failed attempt", "err", err)
		}
		return
	}
	if err := s.markSent(ctx, rows); err != nil {
		// Sent, but not recorded: it may go out twice. Better than not at all.
		s.log.Error("mail sent but not recorded", "kind", kind, "err", err)
	}
}

func (s *Service) markSent(ctx context.Context, rows []row) error {
	now := dbx.Millis(s.now())
	return s.db.InTx(ctx, func(tx *dbx.Tx) error {
		for _, r := range rows {
			if _, err := tx.ExecContext(ctx, `UPDATE mail_outbox SET status = ?, sent_at = ?, batch = ?, attempts = attempts + 1, last_error = ''
				WHERE id = ?`, statusSent, now, rows[0].id, r.id); err != nil {
				return err
			}
			// A link that grants something by itself is kept no longer than needed.
			if templates[r.template].SecretLink {
				if _, err := tx.ExecContext(ctx, `UPDATE mail_outbox SET link = '' WHERE id = ?`, r.id); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// retryDelay is the wait after the n-th failure: 1, 2, 4 … minutes, at most an hour.
func retryDelay(n int) time.Duration {
	d := time.Minute
	for i := 1; i < n && d < time.Hour; i++ {
		d *= 2
	}
	return min(d, time.Hour)
}

// markRetry records a failed attempt: back off, or give up once the retry window, counted from
// the first attempt, has passed.
func (s *Service) markRetry(ctx context.Context, rows []row, reason string) error {
	now := s.now()
	return s.db.InTx(ctx, func(tx *dbx.Tx) error {
		for _, r := range rows {
			first := now
			if r.firstAttempt.Valid {
				first = dbx.Time(r.firstAttempt.Int64)
			}
			attempts := r.attempts + 1
			status := statusQueued
			if now.Sub(first) >= s.retryFor {
				status = statusFailed
			}
			if _, err := tx.ExecContext(ctx, `UPDATE mail_outbox SET status = ?, attempts = ?, next_attempt_at = ?, first_attempt_at = ?, last_error = ?
				WHERE id = ?`, status, attempts, dbx.Millis(now.Add(retryDelay(attempts))), dbx.Millis(first), reason, r.id); err != nil {
				return err
			}
		}
		return nil
	})
}

// markFailed gives up on rows that cannot be sent at all (a template that no longer exists).
func (s *Service) markFailed(ctx context.Context, rows []row, reason string) {
	err := s.db.InTx(ctx, func(tx *dbx.Tx) error {
		for _, r := range rows {
			if _, err := tx.ExecContext(ctx, `UPDATE mail_outbox SET status = ?, attempts = attempts + 1, last_error = ? WHERE id = ?`,
				statusFailed, clip(reason, maxErrorLen), r.id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.log.Error("mail: cannot record the failure", "err", err)
	}
}

// RetryFailed queues the failed mails again, with a new retry window, and returns how many.
func (s *Service) RetryFailed(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE mail_outbox SET status = ?, attempts = 0, first_attempt_at = NULL, next_attempt_at = ? WHERE status = ?`,
		statusQueued, dbx.Millis(s.now()), statusFailed)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if n > 0 && s.jobs != nil {
		s.jobs.Trigger(JobName)
	}
	return int(n), err
}

// Stats counts the outbox.
type Stats struct {
	Queued       int // waiting, including those between retries
	Retrying     int // waiting after at least one failed attempt
	Failed       int // given up
	SentLastHour int // messages sent in the last hour (a digest is one)
}

// Stats returns the counts for the readiness check and metrics.
func (s *Service) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	err := s.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = ? AND attempts > 0 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0)
		FROM mail_outbox`, statusQueued, statusQueued, statusFailed).Scan(&st.Queued, &st.Retrying, &st.Failed)
	if err != nil {
		return st, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT batch) FROM mail_outbox WHERE status = ? AND sent_at > ?`,
		statusSent, dbx.Millis(s.now().Add(-time.Hour))).Scan(&st.SentLastHour)
	return st, err
}

// Errors of Check: mail is degraded, the server still works.
var (
	ErrNotConfigured = errors.New("mail is not configured")
	ErrRetrying      = errors.New("sending mail fails; retrying")
	ErrFailing       = errors.New("some mail could not be sent")
)

// Check is the readiness item "mail": nil when mail works, otherwise why it is degraded. It is
// not critical: the server serves without mail.
func (s *Service) Check(ctx context.Context) error {
	if _, _, _, ok, err := s.channel(ctx); err != nil {
		return err
	} else if !ok {
		return ErrNotConfigured
	}
	st, err := s.Stats(ctx)
	switch {
	case err != nil:
		return err
	case st.Failed > 0:
		return fmt.Errorf("%w: %d", ErrFailing, st.Failed)
	case st.Retrying > 0:
		return fmt.Errorf("%w: %d", ErrRetrying, st.Retrying)
	}
	return nil
}

// SendTest sends the test message to one address right away, outside the outbox, and returns
// the channel's error with the secret settings masked. It is for the settings page.
func (s *Service) SendTest(ctx context.Context, to, locale string) error {
	if !oneAddress(to) {
		return errors.New("notify: the recipient is not one e-mail address")
	}
	ch, cfg, secrets, ok, err := s.channel(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotConfigured
	}
	out, err := s.render.one(locale, TemplateTest, nil, "")
	if err != nil {
		return err
	}
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	err = ch.Send(sendCtx, cfg, extnotify.Message{
		To: extnotify.Recipient{Address: to}, From: s.sender, Subject: out.subject, Text: out.text, HTML: out.html,
		Kind: TemplateTest, Locale: s.render.printer(locale).Lang().String(),
	})
	if err != nil {
		return errors.New(clip(redact(err.Error(), secrets), maxErrorLen))
	}
	return nil
}

// clip cuts s to at most n bytes, at a character boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package settings

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// Mail is the mail server, without its password.
type Mail struct {
	Enabled  bool   `json:"enabled"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Security string `json:"security,omitempty"` // tls | starttls
	Username string `json:"username,omitempty"`
	From     string `json:"from,omitempty"`
}

// The mail security modes.
const (
	SecurityTLS      = "tls"      // TLS from the start, port 465
	SecurityStartTLS = "starttls" // anything else; no STARTTLS means nothing is sent
)

// Platform is the platform settings as administrators see them. Secrets are
// only "set" or "not set".
type Platform struct {
	OrganizationName string
	Mail             Mail
	MailPasswordSet  bool
	// ETag changes whenever any of the above does, the password included.
	ETag string
}

// PlatformChange is a change of the platform settings. Nil fields stay as
// they are.
type PlatformChange struct {
	OrganizationName *string
	Mail             *MailChange
}

// MailChange changes the mail server. Password is write-only: nil keeps it,
// "" removes it (a server without authentication).
type MailChange struct {
	Enabled  *bool
	Host     *string
	Port     *int
	Security *string
	Username *string
	Password *string
	From     *string
}

// The actions of this file, also the audit's.
const (
	ActionSettingsRead   = "settings.read"
	ActionSettingsUpdate = "settings.update"
)

var platformObject = audit.Object{Kind: "settings", ID: "platform", Label: "Platform settings"}

// Platform returns the platform settings. Platform administrators only.
func (s *Service) Platform(ctx context.Context, actor audit.Actor) (Platform, error) {
	if err := s.authorize(ctx, actor, ActionSettingsRead, platformObject); err != nil {
		return Platform{}, err
	}
	return s.platform(ctx, s.db)
}

func (s *Service) platform(ctx context.Context, q dbx.Querier) (Platform, error) {
	var p Platform
	if _, err := getJSON(ctx, q, keyOrgName, &p.OrganizationName); err != nil {
		return p, err
	}
	if _, err := getJSON(ctx, q, keyMail, &p.Mail); err != nil {
		return p, err
	}
	h := sha256.New()
	for _, key := range []string{keyOrgName, keyMail, keyMailPassword} {
		value, _, found, err := row(ctx, q, key)
		if err != nil {
			return p, err
		}
		if key == keyMailPassword {
			p.MailPasswordSet = found
		}
		// A sealed value is new on every write, so replacing the password
		// with itself still changes the ETag. That is right: it was
		// replaced.
		h.Write([]byte(key + "\x00" + value + "\x00"))
	}
	p.ETag = `"settings-` + hex.EncodeToString(h.Sum(nil)[:8]) + `"`
	return p, nil
}

// UpdatePlatform changes the platform settings when ifMatch is the ETag of
// the current ones, and records the change in the audit log in the same
// transaction: which fields, from what to what, the password only as
// "replaced" or "removed". Platform administrators only.
func (s *Service) UpdatePlatform(ctx context.Context, actor audit.Actor, ifMatch string, c PlatformChange) (Platform, error) {
	if err := s.authorize(ctx, actor, ActionSettingsUpdate, platformObject); err != nil {
		return Platform{}, err
	}
	var out Platform
	err := s.db.InTx(ctx, func(tx *dbx.Tx) error {
		if err := tx.Lock(ctx, lockKey); err != nil {
			return err
		}
		cur, err := s.platform(ctx, tx)
		if err != nil {
			return err
		}
		if strings.TrimSpace(ifMatch) != cur.ETag {
			return ErrPreconditionFailed
		}
		change := map[string]any{}
		diff := func(field string, from, to any) {
			if from != to {
				change[field] = map[string]any{"from": from, "to": to}
			}
		}
		now := s.now()

		if c.OrganizationName != nil {
			name := strings.TrimSpace(*c.OrganizationName)
			if utf8.RuneCountInString(name) > 64 {
				return &ValidationError{Field: "organization.name", Code: "too_long"}
			}
			if name != cur.OrganizationName {
				diff("organization.name", cur.OrganizationName, name)
				if err := putJSON(ctx, tx, keyOrgName, name, actor, now); err != nil {
					return err
				}
			}
		}

		if m := c.Mail; m != nil {
			next := cur.Mail
			set := func(dst *string, src *string) {
				if src != nil {
					*dst = strings.TrimSpace(*src)
				}
			}
			if m.Enabled != nil {
				next.Enabled = *m.Enabled
			}
			set(&next.Host, m.Host)
			set(&next.Security, m.Security)
			set(&next.Username, m.Username)
			set(&next.From, m.From)
			if m.Port != nil {
				next.Port = *m.Port
			}
			if next.Security == "" && next.Port != 0 {
				next.Security = SecurityStartTLS
				if next.Port == 465 {
					next.Security = SecurityTLS
				}
			}
			if err := validateMail(next); err != nil {
				return err
			}
			if next != cur.Mail {
				diff("mail.enabled", cur.Mail.Enabled, next.Enabled)
				diff("mail.host", cur.Mail.Host, next.Host)
				diff("mail.port", cur.Mail.Port, next.Port)
				diff("mail.security", cur.Mail.Security, next.Security)
				diff("mail.username", cur.Mail.Username, next.Username)
				diff("mail.from", cur.Mail.From, next.From)
				if err := putJSON(ctx, tx, keyMail, next, actor, now); err != nil {
					return err
				}
			}
			switch {
			case m.Password == nil:
			case *m.Password == "" && cur.MailPasswordSet:
				change["mail.password"] = "removed"
				if err := remove(ctx, tx, keyMailPassword); err != nil {
					return err
				}
			case *m.Password != "":
				change["mail.password"] = "replaced"
				if err := s.putSealed(ctx, tx, keyMailPassword, []byte(*m.Password), actor); err != nil {
					return err
				}
			}
		}

		if len(change) > 0 {
			if _, err := s.log.Append(ctx, tx, audit.Entry{Actor: actor, Action: ActionSettingsUpdate, Object: platformObject, Change: change}); err != nil {
				return err
			}
		}
		out, err = s.platform(ctx, tx)
		return err
	})
	if err != nil {
		return Platform{}, err
	}
	return out, nil
}

func validateMail(m Mail) error {
	if m.Port < 0 || m.Port > 65535 {
		return &ValidationError{Field: "mail.port", Code: "invalid"}
	}
	if m.Security != "" && m.Security != SecurityTLS && m.Security != SecurityStartTLS {
		return &ValidationError{Field: "mail.security", Code: "invalid"}
	}
	if len(m.Host) > 255 || strings.ContainsAny(m.Host, " /:@") {
		return &ValidationError{Field: "mail.host", Code: "invalid"}
	}
	if m.From != "" {
		if a, err := mail.ParseAddress(m.From); err != nil || a.Address != m.From {
			return &ValidationError{Field: "mail.from", Code: "invalid"}
		}
	}
	if m.Enabled {
		switch {
		case m.Host == "":
			return &ValidationError{Field: "mail.host", Code: "required"}
		case m.Port == 0:
			return &ValidationError{Field: "mail.port", Code: "required"}
		case m.From == "":
			return &ValidationError{Field: "mail.from", Code: "required"}
		}
	}
	return nil
}

// MailConfig is the mail server with its password, for the code that sends
// mail. It never goes into a response or a log.
type MailConfig struct {
	Mail
	Password string
}

// MailServer returns the mail server with its password opened. It is for
// the notify package, not for an API: there is no permission check.
func (s *Service) MailServer(ctx context.Context) (MailConfig, error) {
	var c MailConfig
	if _, err := getJSON(ctx, s.db, keyMail, &c.Mail); err != nil {
		return c, err
	}
	pw, found, err := s.getSealed(ctx, s.db, keyMailPassword)
	if err != nil {
		return c, err
	}
	if found {
		c.Password = string(pw)
	}
	return c, nil
}

// OrganizationName is the organization's name, "" when not set. Public: the
// sign-in page shows it.
func (s *Service) OrganizationName(ctx context.Context) (string, error) {
	var name string
	_, err := getJSON(ctx, s.db, keyOrgName, &name)
	return name, err
}

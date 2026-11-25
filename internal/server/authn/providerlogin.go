// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package authn

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/code2rich/rigdepot/ext/identity"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// Signing in through an identity source (docs/architecture/06-server.md#oidc-和-gitlab):
//
//  1. BeginProviderLogin asks the source where to send the browser and seals
//     the source's state, with the source's id, where to land afterwards and
//     an expiry, into a cookie value (AES-256-GCM under a key derived from
//     the master key). Nothing is stored on the server.
//  2. CompleteProviderLogin opens the cookie, has the source check the
//     callback and return the identity, and links it to an account: first by
//     (source, subject); else by the e-mail address, but only when the source
//     says it is verified and exactly one account has it; else a new account
//     in status pending, which still has to ask to join. A disabled account
//     is refused, whichever way it was found. The sign-in, failed or not, is
//     in the audit log.

// LoginStateTTL is how long a sign-in through an identity source may take
// from start to callback.
const LoginStateTTL = 10 * time.Minute

// LoginError is a refused sign-in through an identity source. Code is what
// the login page is told (/login?error=<code>); Err is the cause, for the
// server log, with the client secret masked.
type LoginError struct {
	Code string
	Err  error
}

func (e *LoginError) Error() string {
	if e.Err == nil {
		return "authn: " + e.Code
	}
	return "authn: " + e.Code + ": " + e.Err.Error()
}

func (e *LoginError) Unwrap() error { return e.Err }

// The codes of LoginError.
const (
	LoginUnavailable   = "auth.provider_unavailable" // no such source, or it is off
	LoginFailed        = "auth.provider_failed"      // the source failed, or its answer cannot be trusted
	LoginDeclined      = "auth.declined"             // the person said no at the provider
	LoginStateInvalid  = "auth.state_invalid"        // stale, forged or from another browser; start again
	LoginDisabled      = "user.disabled"             // the account is disabled
	LoginEmailConflict = "auth.email_conflict"       // several accounts have the verified address
)

// The reasons of refused sign-ins through a source in the audit log.
const (
	reasonState    = "state_invalid"
	reasonDeclined = "declined"
	reasonProvider = "provider_failed"
	reasonConflict = "email_conflict"
)

// loginState is what the cookie carries.
type loginState struct {
	Provider string `json:"p"`
	ReturnTo string `json:"r,omitempty"`
	Expires  int64  `json:"e"` // milliseconds
	State    []byte `json:"s"`
}

func (s *Service) stateAEAD() (cipher.AEAD, error) {
	if s.keys == nil {
		return nil, errors.New("authn: no master key for the sign-in state")
	}
	block, err := aes.NewCipher(s.keys.Derive("identity sign-in state"))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func stateAD(providerID string) []byte { return []byte("identity sign-in/" + providerID) }

func (s *Service) sealState(st loginState) (string, error) {
	aead, err := s.stateAEAD()
	if err != nil {
		return "", err
	}
	plain, err := json.Marshal(st)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, plain, stateAD(st.Provider))), nil
}

// openState opens a cookie value sealed for providerID; false when it does
// not open, belongs to another source or expired.
func (s *Service) openState(value, providerID string) (loginState, bool) {
	var st loginState
	aead, err := s.stateAEAD()
	if err != nil || len(value) > 8192 {
		return st, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) < aead.NonceSize() {
		return st, false
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], stateAD(providerID))
	if err != nil || json.Unmarshal(plain, &st) != nil || st.Provider != providerID {
		return st, false
	}
	if !s.now().Before(dbx.Time(st.Expires)) {
		return st, false
	}
	return st, true
}

// ReturnPath keeps a return_to that is a path on this server ("/catalog")
// and drops anything else ("" then): another site, a scheme-relative
// address, a backslash some browsers read as a slash.
func ReturnPath(p string) string {
	if p == "" || len(p) > 1024 || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\\r\n\t") {
		return ""
	}
	u, err := url.Parse(p)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil {
		return ""
	}
	return p
}

// ProviderLogin is a started sign-in.
type ProviderLogin struct {
	RedirectURL string // the identity provider's authorization address
	Cookie      string // the sealed state for the browser to keep until the callback
}

// usable returns an enabled identity source and its configuration.
func (s *Service) usable(ctx context.Context, providerID string) (providerRow, identity.Provider, identity.Config, error) {
	if providerID == LocalProviderID {
		return providerRow{}, nil, identity.Config{}, &LoginError{Code: LoginUnavailable}
	}
	r, err := provider(ctx, s.db, providerID)
	if errors.Is(err, ErrNotFound) {
		return r, nil, identity.Config{}, &LoginError{Code: LoginUnavailable}
	}
	if err != nil {
		return r, nil, identity.Config{}, err
	}
	kind, ok := s.kind(r.kind)
	if !r.enabled || !ok {
		return r, nil, identity.Config{}, &LoginError{Code: LoginUnavailable}
	}
	cfg, err := s.config(r)
	return r, kind, cfg, err
}

// BeginProviderLogin starts signing in through the identity source with this
// id. returnTo is where to land afterwards; only a path on this server is
// kept.
func (s *Service) BeginProviderLogin(ctx context.Context, providerID, returnTo string) (ProviderLogin, error) {
	r, kind, cfg, err := s.usable(ctx, providerID)
	if err != nil {
		return ProviderLogin{}, err
	}
	redirect, state, err := kind.Begin(ctx, cfg, s.callbackURL(r.id))
	if err != nil {
		return ProviderLogin{}, &LoginError{Code: LoginFailed, Err: errors.New(mask(err.Error(), cfg))}
	}
	cookie, err := s.sealState(loginState{Provider: r.id, ReturnTo: ReturnPath(returnTo), Expires: dbx.Millis(s.now().Add(LoginStateTTL)), State: state})
	if err != nil {
		return ProviderLogin{}, err
	}
	return ProviderLogin{RedirectURL: redirect, Cookie: cookie}, nil
}

// ProviderSignIn is a finished sign-in through an identity source.
type ProviderSignIn struct {
	SignedIn
	// ReturnTo is the path the sign-in began with; "" when none was given.
	ReturnTo string
	// Linked says how the identity met its account: "identity" (signed in
	// with it before), "email" (linked now by the verified address) or
	// "new" (a new account, pending).
	Linked string
}

// The ways an identity meets its account.
const (
	LinkedIdentity = "identity"
	LinkedEmail    = "email"
	LinkedNew      = "new"
)

// CompleteProviderLogin finishes the sign-in that cookie began, with the
// callback's query. Every failure is a *LoginError (or a fault) and signs
// nobody in.
func (s *Service) CompleteProviderLogin(ctx context.Context, providerID, cookie string, query url.Values) (ProviderSignIn, error) {
	r, kind, cfg, err := s.usable(ctx, providerID)
	if err != nil {
		return ProviderSignIn{}, err
	}
	object := providerObject(r)
	refuse := func(code, reason string, obj audit.Object, cause error) (ProviderSignIn, error) {
		change := map[string]any{"provider": r.id}
		_, aerr := s.audit.Record(ctx, audit.Entry{
			Category: audit.CategoryLogin, Method: r.kind, Actor: audit.Actor{Origin: audit.OriginWeb},
			Action: ActionLogin, Object: obj, Change: change, Result: audit.ResultDenied, Reason: reason,
		})
		if cause != nil {
			cause = errors.New(mask(cause.Error(), cfg))
		}
		return ProviderSignIn{}, refused(&LoginError{Code: code, Err: cause}, aerr)
	}
	st, ok := s.openState(cookie, r.id)
	if !ok {
		return refuse(LoginStateInvalid, reasonState, object, nil)
	}
	id, err := kind.Complete(ctx, cfg, st.State, query)
	switch {
	case errors.Is(err, identity.ErrDeclined):
		return refuse(LoginDeclined, reasonDeclined, object, nil)
	case errors.Is(err, identity.ErrState):
		return refuse(LoginStateInvalid, reasonState, object, nil)
	case err != nil:
		return refuse(LoginFailed, reasonProvider, object, err)
	case strings.TrimSpace(id.Subject) == "" || len(id.Subject) > 255:
		return refuse(LoginFailed, reasonProvider, object, errors.New("the identity has no usable subject"))
	}
	email := ""
	if a, err := mail.ParseAddress(strings.TrimSpace(id.Email)); err == nil && a.Name == "" && len(a.Address) <= 254 {
		email = strings.ToLower(a.Address)
	}

	var out ProviderSignIn
	var denied *LoginError
	var deniedObject audit.Object
	err = s.db.InTx(ctx, func(tx *dbx.Tx) error {
		u, linked, err := s.accountFor(ctx, tx, r, id, email)
		var le *LoginError
		if errors.As(err, &le) {
			denied, deniedObject = le, userObject(u.ID, u.Username)
			if u.ID == "" {
				deniedObject = audit.Object{Kind: "user", Label: email}
			}
			return nil
		}
		if err != nil {
			return err
		}
		if out.SignedIn, err = s.startSession(ctx, tx, u.ID); err != nil {
			return err
		}
		out.User, out.ReturnTo, out.Linked = u, st.ReturnTo, linked
		change := map[string]any{"provider": r.id, "linked": linked}
		if linked == LinkedNew {
			change["status"] = string(u.Status)
		}
		_, err = s.audit.Append(ctx, tx, audit.Entry{
			Category: audit.CategoryLogin, Method: r.kind, Actor: webActor(u.ID),
			Action: ActionLogin, Object: userObject(u.ID, u.Username), Change: change,
		})
		return err
	})
	if err != nil {
		return ProviderSignIn{}, err
	}
	if denied != nil {
		reason := reasonDisabled
		if denied.Code == LoginEmailConflict {
			reason = reasonConflict
		}
		return refuse(denied.Code, reason, deniedObject, nil)
	}
	return out, nil
}

// accountFor finds or makes the account of an identity inside tx, and
// records the identity's sign-in. A refusal is a *LoginError with the
// account it is about, when there is one.
func (s *Service) accountFor(ctx context.Context, tx *dbx.Tx, r providerRow, id identity.Identity, email string) (access.User, string, error) {
	now := s.millis()
	// 1. This identity signed in before.
	var userID string
	err := tx.QueryRowContext(ctx, `SELECT user_id FROM identities WHERE provider = ? AND subject = ?`, r.id, id.Subject).Scan(&userID)
	switch {
	case err == nil:
		u, err := s.access.GetUser(ctx, access.System, userID)
		if err != nil {
			return u, "", err
		}
		if !signsIn(u) {
			return u, "", &LoginError{Code: LoginDisabled}
		}
		_, err = tx.ExecContext(ctx, `UPDATE identities SET email = ?, last_login_at = ? WHERE provider = ? AND subject = ?`, email, now, r.id, id.Subject)
		return u, LinkedIdentity, err
	case !isNoRows(err):
		return access.User{}, "", err
	}

	link := func(u access.User) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO identities (id, user_id, provider, subject, email, created_at, last_login_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			dbx.NewID(), u.ID, r.id, id.Subject, email, now, now)
		return err
	}
	// 2. An account with the address, when the source vouches for it.
	if email != "" && id.EmailVerified {
		matches, err := s.usersByEmail(ctx, email)
		if err != nil {
			return access.User{}, "", err
		}
		switch len(matches) {
		case 0:
		case 1:
			u := matches[0]
			if !signsIn(u) {
				return u, "", &LoginError{Code: LoginDisabled}
			}
			return u, LinkedEmail, link(u)
		default:
			// Which one is the person's is for an administrator to decide.
			return access.User{}, "", &LoginError{Code: LoginEmailConflict}
		}
	}
	// 3. Someone new: an account that still has to ask to join. An
	// address the source did not verify stays with the identity only, so
	// that nobody can claim another person's address through a source that
	// does not check it.
	n := access.NewUser{DisplayName: displayName(id, email), Status: access.StatusPending}
	if email != "" && id.EmailVerified {
		n.Email, n.EmailVerified = email, true
	}
	var u access.User
	for i, name := range usernameCandidates(id, email) {
		n.Username = name
		u, err = s.access.CreateUser(ctx, tx, access.System, n)
		if errors.Is(err, access.ErrUsernameTaken) && i < maxNameTries-1 {
			continue
		}
		break
	}
	if err != nil {
		return u, "", err
	}
	return u, LinkedNew, link(u)
}

func signsIn(u access.User) bool {
	return u.Status == access.StatusActive || u.Status == access.StatusPending
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// usersByEmail returns the accounts whose address is exactly email (lower
// case).
//
// TODO: access has no lookup by address yet; ListUsers' search plus an exact
// comparison does the same.
func (s *Service) usersByEmail(ctx context.Context, email string) ([]access.User, error) {
	q := email
	if len(q) > 100 {
		q = q[:100] // the search's limit; the exact comparison below decides
	}
	us, err := s.access.ListUsers(ctx, access.System, access.UserFilter{Query: q})
	if err != nil {
		return nil, err
	}
	var out []access.User
	for _, u := range us {
		if strings.EqualFold(u.Email, email) {
			out = append(out, u)
		}
	}
	return out, nil
}

func displayName(id identity.Identity, email string) string {
	name := strings.TrimSpace(id.DisplayName)
	if name == "" {
		name = strings.TrimSpace(id.Username)
	}
	if name == "" && email != "" {
		name, _, _ = strings.Cut(email, "@")
	}
	if !utf8.ValidString(name) {
		name = ""
	}
	if utf8.RuneCountInString(name) > 64 {
		name = string([]rune(name)[:64])
	}
	return name
}

const maxNameTries = 20

var notNameChars = regexp.MustCompile(`[^a-z0-9._-]+`)

// usernameCandidates are account names for a new account, in the order
// tried: the name at the source, the address's local part, then "user";
// each followed by -2, -3… when taken.
func usernameCandidates(id identity.Identity, email string) []string {
	clean := func(s string) string {
		s = notNameChars.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
		s = strings.Trim(s, "._-")
		if len(s) > 28 {
			s = strings.Trim(s[:28], "._-")
		}
		if len(s) < 2 {
			return ""
		}
		return s
	}
	local, _, _ := strings.Cut(email, "@")
	base := "user"
	for _, c := range []string{id.Username, local} {
		if b := clean(c); b != "" {
			base = b
			break
		}
	}
	out := []string{base}
	for i := 2; len(out) < maxNameTries-1; i++ {
		out = append(out, base+"-"+strconv.Itoa(i))
	}
	// Last resort: random, practically never taken.
	return append(out, fmt.Sprintf("user-%s", strings.ToLower(rand.Text()[:10])))
}

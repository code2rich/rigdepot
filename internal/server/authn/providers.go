// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package authn

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
	jskind "github.com/santhosh-tekuri/jsonschema/v6/kind"

	"github.com/code2rich/rigdepot/ext/identity"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/masterkey"
)

// The sign-in methods (docs/architecture/06-server.md#oidc-和-gitlab). Local
// accounts are the method with LocalProviderID; every other one is an
// identity source of a kind compiled into the server (ext/identity),
// configured by a platform administrator in the table identity_providers.
// Its client secret is sealed with the master key the moment it arrives and
// is never part of any result of this package: Provider only says whether
// one is set.
const (
	LocalProviderID = "local"
	KindLocal       = "local"
)

// The audit log's actions of the sign-in methods.
const (
	ActionProviderCreate = "identity_provider.create"
	ActionProviderUpdate = "identity_provider.update"
	ActionProviderDelete = "identity_provider.delete"
	ActionProviderTest   = "identity_provider.test"
)

// More failures of this package.
var (
	// ErrPreconditionFailed: the method changed since the caller read it.
	ErrPreconditionFailed = &Error{Kind: KindPrecondition, Code: "precondition.failed"}
	// ErrPreconditionRequired: a change without If-Match.
	ErrPreconditionRequired = &Error{Kind: KindPreconditionRequired, Code: "precondition.required"}
	// ErrLastMethod: the change would leave no enabled sign-in method.
	ErrLastMethod = &Error{Kind: KindConflict, Code: "identity.last_method"}
	// ErrLocalFixed: local accounts can only be turned on and off.
	ErrLocalFixed = &Error{Kind: KindConflict, Code: "identity.local_fixed"}
	// ErrMethodDisabled: signing in with a method that is turned off.
	ErrMethodDisabled = &Error{Kind: KindDenied, Code: "auth.method_disabled"}
)

// Provider is a sign-in method as administrators see it.
type Provider struct {
	ID              string
	Kind            string
	Name            string
	Enabled         bool
	Config          map[string]any // the settings without the secret
	HasClientSecret bool
	ETag            string
}

// LoginMethod is an enabled sign-in method as the login page shows it.
type LoginMethod struct {
	ID, Kind, Name string
}

// NewProvider is a sign-in method to add.
type NewProvider struct {
	Kind         string
	Name         string
	Enabled      *bool // nil is on
	Config       map[string]any
	ClientSecret *string
}

// ProviderChange changes a sign-in method. Nil fields stay as they are; a
// ClientSecret of "" removes the secret.
type ProviderChange struct {
	Name         *string
	Enabled      *bool
	Config       map[string]any
	ClientSecret *string
}

// providerRow is a row of identity_providers.
type providerRow struct {
	id, kind, name string
	enabled        bool
	position       int
	config         map[string]any
	secret         string // sealed, encoded; "" when none
	revision       int64
	stored         bool // false for the local method before anyone changed it
	broken         bool // the stored settings do not parse
}

func (r providerRow) etag() string {
	return `"p-` + r.id + `-` + strconv.FormatInt(r.revision, 10) + `"`
}

func (r providerRow) view() Provider {
	cfg := r.config
	if cfg == nil {
		cfg = map[string]any{}
	}
	return Provider{ID: r.id, Kind: r.kind, Name: r.name, Enabled: r.enabled, Config: cfg, HasClientSecret: r.secret != "", ETag: r.etag()}
}

// localName is the name of the local method. TODO(i18n): render it in the
// request's language once locales/server.json is wired into the API.
const localName = "Local account"

func localDefault() providerRow {
	return providerRow{id: LocalProviderID, kind: KindLocal, name: localName, enabled: true, config: map[string]any{}}
}

const providerColumns = `id, kind, name, enabled, position, config, client_secret, revision`

func scanProvider(sc interface{ Scan(...any) error }) (providerRow, error) {
	var r providerRow
	var cfg string
	if err := sc.Scan(&r.id, &r.kind, &r.name, &r.enabled, &r.position, &cfg, &r.secret, &r.revision); err != nil {
		return r, err
	}
	r.stored = true
	dec := json.NewDecoder(strings.NewReader(cfg))
	dec.UseNumber()
	if err := dec.Decode(&r.config); err != nil || r.config == nil {
		// Unknown, not empty: a source whose settings cannot be read is
		// listed for administrators to fix or remove, and not used.
		r.config, r.broken = nil, true
	}
	if r.id == LocalProviderID {
		r.name = localName
	}
	return r, nil
}

// providers returns every method in display order: local accounts first,
// then the identity sources in the order they were added.
func providers(ctx context.Context, q dbx.Querier) ([]providerRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+providerColumns+` FROM identity_providers ORDER BY position, created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []providerRow{localDefault()}
	for rows.Next() {
		r, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		if r.id == LocalProviderID {
			out[0] = r
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// provider returns one method; ErrNotFound when there is none.
func provider(ctx context.Context, q dbx.Querier, id string) (providerRow, error) {
	r, err := scanProvider(q.QueryRowContext(ctx, `SELECT `+providerColumns+` FROM identity_providers WHERE id = ?`, id))
	switch {
	case errors.Is(err, sql.ErrNoRows) && id == LocalProviderID:
		return localDefault(), nil
	case errors.Is(err, sql.ErrNoRows):
		return r, ErrNotFound
	}
	return r, err
}

// Kinds returns the kinds of identity sources compiled into the server, by
// ID.
func (s *Service) Kinds() []identity.Provider { return s.kindList }

func (s *Service) kind(id string) (identity.Provider, bool) {
	p, ok := s.kinds[id]
	return p, ok
}

// admin checks that c may manage the sign-in methods: a platform
// administrator (access's settings.manage). Writes also need a browser
// session: client secrets are entered on the web only, never by a program or
// an AI tool.
func (s *Service) admin(ctx context.Context, c Caller, write bool) error {
	if c.Actor.UserID == "" {
		return ErrUnauthenticated
	}
	if write && (!c.browser() || c.Actor.ViaTool != "") {
		return ErrDenied
	}
	d := s.access.Can(ctx, c.Actor, access.ActionSettingsManage, access.TargetPlatform())
	if d.Err != nil {
		return d.Err
	}
	if !d.Allowed {
		return ErrDenied
	}
	return nil
}

// ListProviders returns every sign-in method, local accounts first.
// Platform administrators only.
func (s *Service) ListProviders(ctx context.Context, c Caller) ([]Provider, error) {
	if err := s.admin(ctx, c, false); err != nil {
		return nil, err
	}
	rows, err := providers(ctx, s.db)
	if err != nil {
		return nil, err
	}
	out := make([]Provider, len(rows))
	for i, r := range rows {
		out[i] = r.view()
	}
	return out, nil
}

// GetProvider returns one sign-in method. Platform administrators only.
func (s *Service) GetProvider(ctx context.Context, c Caller, id string) (Provider, error) {
	if err := s.admin(ctx, c, false); err != nil {
		return Provider{}, err
	}
	r, err := provider(ctx, s.db, id)
	if err != nil {
		return Provider{}, err
	}
	return r.view(), nil
}

// LoginMethods returns the enabled sign-in methods in display order, for
// the login page. Sources whose kind is not compiled into this server are
// left out.
func (s *Service) LoginMethods(ctx context.Context) ([]LoginMethod, error) {
	rows, err := providers(ctx, s.db)
	if err != nil {
		return nil, err
	}
	out := []LoginMethod{}
	for _, r := range rows {
		if !r.enabled || r.broken {
			continue
		}
		if _, ok := s.kind(r.kind); !ok && r.id != LocalProviderID {
			continue
		}
		out = append(out, LoginMethod{ID: r.id, Kind: r.kind, Name: r.name})
	}
	return out, nil
}

// localEnabled reports whether local accounts may sign in with a password.
func (s *Service) localEnabled(ctx context.Context) (bool, error) {
	r, err := provider(ctx, s.db, LocalProviderID)
	return r.enabled, err
}

// schemas caches the compiled configuration schemas by kind.
var schemas sync.Map // kind ID -> *jsonschema.Schema

func compiled(p identity.Provider) (*jsonschema.Schema, error) {
	if sch, ok := schemas.Load(p.ID()); ok {
		return sch.(*jsonschema.Schema), nil
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(p.ConfigSchema()))
	if err != nil {
		return nil, fmt.Errorf("authn: the settings schema of %s: %w", p.ID(), err)
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(jsonschema.SchemeURLLoader{}) // nothing is fetched
	c.AssertFormat()
	id := "urn:rigdepot:identity:" + p.ID()
	if err := c.AddResource(id, doc); err != nil {
		return nil, err
	}
	sch, err := c.Compile(id)
	if err != nil {
		return nil, fmt.Errorf("authn: the settings schema of %s: %w", p.ID(), err)
	}
	schemas.Store(p.ID(), sch)
	return sch, nil
}

// checkConfig validates settings against the kind's schema and its own
// rules. The field errors name config.<setting>.
func checkConfig(p identity.Provider, cfg map[string]any) error {
	sch, err := compiled(p)
	if err != nil {
		return err
	}
	// The schema validator wants plain JSON values.
	raw, err := json.Marshal(cfg)
	if err != nil {
		return invalid(access.FieldError{Field: "config", Code: "type"})
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return invalid(access.FieldError{Field: "config", Code: "type"})
	}
	if err := sch.Validate(v); err != nil {
		var verr *jsonschema.ValidationError
		if !errors.As(err, &verr) {
			return err
		}
		return invalid(schemaFields(verr)...)
	}
	if err := p.Validate(identity.Config{Settings: cfg}); err != nil {
		var ce *identity.ConfigError
		if errors.As(err, &ce) {
			return invalid(access.FieldError{Field: "config." + ce.Field, Code: ce.Code})
		}
		return invalid(access.FieldError{Field: "config", Code: "invalid"})
	}
	return nil
}

// schemaFields turns a validation error into field errors: the setting it is
// about and a short code.
func schemaFields(e *jsonschema.ValidationError) []access.FieldError {
	var out []access.FieldError
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) > 0 {
			for _, c := range e.Causes {
				walk(c)
			}
			return
		}
		field := "config"
		for _, tok := range e.InstanceLocation {
			field += "." + tok
		}
		code := "invalid"
		switch k := e.ErrorKind.(type) {
		case *jskind.Required:
			code = "required"
			if len(k.Missing) > 0 {
				field += "." + k.Missing[0]
			}
		case *jskind.AdditionalProperties:
			code = "unknown"
			if len(k.Properties) > 0 {
				field += "." + k.Properties[0]
			}
		case *jskind.Type:
			code = "type"
		case *jskind.Format:
			code = "format"
		case *jskind.MaxLength, *jskind.MaxItems:
			code = "too_long"
		case *jskind.MinLength:
			code = "required"
		case *jskind.Pattern:
			code = "pattern"
		}
		out = append(out, access.FieldError{Field: field, Code: code})
	}
	walk(e)
	return out
}

func checkName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 64 || !utf8.ValidString(name) {
		return "", invalid(access.FieldError{Field: "name", Code: "length"})
	}
	return name, nil
}

// secretAD binds a sealed client secret to its method.
func secretAD(id string) []byte { return []byte("identity_providers/" + id + "/client_secret") }

func (s *Service) seal(id, secret string) (string, error) {
	if secret == "" {
		return "", nil
	}
	if s.keys == nil {
		return "", errors.New("authn: no master key to seal the client secret with")
	}
	sealed, err := s.keys.Seal([]byte(secret), secretAD(id))
	if err != nil {
		return "", err
	}
	return sealed.Encode(), nil
}

// config returns the configuration a provider gets: the settings and the
// opened client secret.
func (s *Service) config(r providerRow) (identity.Config, error) {
	if r.broken {
		return identity.Config{}, fmt.Errorf("authn: identity provider %s: the stored settings do not parse", r.id)
	}
	cfg := identity.Config{Settings: r.config}
	if r.secret == "" {
		return cfg, nil
	}
	if s.keys == nil {
		return cfg, errors.New("authn: no master key to open the client secret with")
	}
	sealed, err := masterkey.Decode(r.secret)
	if err != nil {
		return cfg, fmt.Errorf("authn: identity provider %s: %w", r.id, err)
	}
	plain, err := s.keys.Open(sealed, secretAD(r.id))
	if err != nil {
		return cfg, fmt.Errorf("authn: identity provider %s: the client secret: %w", r.id, err)
	}
	cfg.ClientSecret = string(plain)
	return cfg, nil
}

func providerObject(r providerRow) audit.Object {
	return audit.Object{Kind: "identity_provider", ID: r.id, Label: r.name}
}

// secretChange is how the audit log records a client secret: never its value.
func secretChange(before, after string) any {
	switch {
	case before == "" && after == "":
		return nil
	case before == "":
		return "set"
	case after == "":
		return "removed"
	}
	return "replaced"
}

// enabledCount counts the enabled methods among rows, with the one whose id
// is changed counted as enabled.
func enabledCount(rows []providerRow, changed string, enabled bool) int {
	n := 0
	for _, r := range rows {
		on := r.enabled
		if r.id == changed {
			on = enabled
		}
		if on {
			n++
		}
	}
	return n
}

// CreateProvider adds an identity source. Platform administrators only, from
// the browser. The client secret is sealed before it is stored.
func (s *Service) CreateProvider(ctx context.Context, c Caller, n NewProvider) (Provider, error) {
	if err := s.admin(ctx, c, true); err != nil {
		return Provider{}, err
	}
	kind, ok := s.kind(n.Kind)
	if !ok {
		return Provider{}, invalid(access.FieldError{Field: "kind", Code: "enum"})
	}
	name, err := checkName(n.Name)
	if err != nil {
		return Provider{}, err
	}
	if n.Config == nil {
		n.Config = map[string]any{}
	}
	if err := checkConfig(kind, n.Config); err != nil {
		return Provider{}, err
	}
	r := providerRow{id: dbx.NewID(), kind: kind.ID(), name: name, enabled: n.Enabled == nil || *n.Enabled, config: n.Config, revision: 1, stored: true}
	if n.ClientSecret != nil {
		if r.secret, err = s.seal(r.id, *n.ClientSecret); err != nil {
			return Provider{}, err
		}
	}
	cfg, err := json.Marshal(r.config)
	if err != nil {
		return Provider{}, err
	}
	err = s.db.InTx(ctx, func(tx *dbx.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) + 1 FROM identity_providers`).Scan(&r.position); err != nil {
			return err
		}
		now := s.millis()
		if _, err := tx.ExecContext(ctx, `INSERT INTO identity_providers (`+providerColumns+`, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.id, r.kind, r.name, r.enabled, r.position, string(cfg), r.secret, r.revision, now, now); err != nil {
			return err
		}
		change := map[string]any{"kind": r.kind, "name": r.name, "enabled": r.enabled, "config": r.config}
		if v := secretChange("", r.secret); v != nil {
			change["client_secret"] = v
		}
		_, err := s.audit.Append(ctx, tx, audit.Entry{Actor: auditActor(c.Actor), Action: ActionProviderCreate, Object: providerObject(r), Change: change})
		return err
	})
	if err != nil {
		return Provider{}, err
	}
	return r.view(), nil
}

// UpdateProvider changes a sign-in method when ifMatch is its current ETag.
// Local accounts can only be turned on and off. Turning off the last enabled
// method is refused. Platform administrators only, from the browser.
func (s *Service) UpdateProvider(ctx context.Context, c Caller, id, ifMatch string, ch ProviderChange) (Provider, error) {
	if err := s.admin(ctx, c, true); err != nil {
		return Provider{}, err
	}
	if ifMatch == "" {
		return Provider{}, ErrPreconditionRequired
	}
	var out providerRow
	err := s.db.InTx(ctx, func(tx *dbx.Tx) error {
		all, err := providers(ctx, tx)
		if err != nil {
			return err
		}
		var r providerRow
		found := false
		for _, x := range all {
			if x.id == id {
				r, found = x, true
			}
		}
		if !found {
			return ErrNotFound
		}
		if ifMatch != "*" && ifMatch != r.etag() {
			return ErrPreconditionFailed
		}
		before := r
		change := map[string]any{}
		if r.id == LocalProviderID && (ch.Name != nil || ch.Config != nil || ch.ClientSecret != nil) {
			return ErrLocalFixed
		}
		if ch.Name != nil {
			if r.name, err = checkName(*ch.Name); err != nil {
				return err
			}
			if r.name != before.name {
				change["name"] = map[string]any{"from": before.name, "to": r.name}
			}
		}
		if ch.Config != nil {
			kind, ok := s.kind(r.kind)
			if !ok {
				return invalid(access.FieldError{Field: "kind", Code: "enum"})
			}
			if err := checkConfig(kind, ch.Config); err != nil {
				return err
			}
			r.config = ch.Config
			change["config"] = map[string]any{"from": before.config, "to": r.config}
		}
		if ch.ClientSecret != nil {
			if r.secret, err = s.seal(r.id, *ch.ClientSecret); err != nil {
				return err
			}
			if v := secretChange(before.secret, r.secret); v != nil {
				change["client_secret"] = v
			}
		}
		if ch.Enabled != nil && *ch.Enabled != r.enabled {
			if !*ch.Enabled && enabledCount(all, r.id, false) == 0 {
				return ErrLastMethod
			}
			r.enabled = *ch.Enabled
			change["enabled"] = r.enabled
		}
		r.revision++
		cfg, err := json.Marshal(r.config)
		if err != nil {
			return err
		}
		now := s.millis()
		if r.stored {
			_, err = tx.ExecContext(ctx, `UPDATE identity_providers SET name = ?, enabled = ?, config = ?, client_secret = ?, revision = ?, updated_at = ? WHERE id = ? AND revision = ?`,
				r.name, r.enabled, string(cfg), r.secret, r.revision, now, r.id, before.revision)
		} else {
			// The local method's first change stores its row.
			_, err = tx.ExecContext(ctx, `INSERT INTO identity_providers (`+providerColumns+`, created_at, updated_at) VALUES (?, ?, ?, ?, 0, ?, '', ?, ?, ?)`,
				r.id, r.kind, r.name, r.enabled, string(cfg), r.revision, now, now)
		}
		if err != nil {
			return err
		}
		r.stored = true
		out = r
		_, err = s.audit.Append(ctx, tx, audit.Entry{Actor: auditActor(c.Actor), Action: ActionProviderUpdate, Object: providerObject(r), Change: change})
		return err
	})
	if err != nil {
		return Provider{}, err
	}
	return out.view(), nil
}

// DeleteProvider removes an identity source and unlinks the accounts that
// signed in with it; they keep existing and can still use their other
// methods. Local accounts cannot be removed, only turned off. Removing the
// last enabled method is refused. Platform administrators only, from the
// browser.
func (s *Service) DeleteProvider(ctx context.Context, c Caller, id string) error {
	if err := s.admin(ctx, c, true); err != nil {
		return err
	}
	if id == LocalProviderID {
		return ErrLocalFixed
	}
	return s.db.InTx(ctx, func(tx *dbx.Tx) error {
		all, err := providers(ctx, tx)
		if err != nil {
			return err
		}
		var r providerRow
		found := false
		for _, x := range all {
			if x.id == id {
				r, found = x, true
			}
		}
		if !found {
			return ErrNotFound
		}
		if r.enabled && enabledCount(all, r.id, false) == 0 {
			return ErrLastMethod
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM identities WHERE provider = ?`, id)
		if err != nil {
			return err
		}
		unlinked, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM identity_providers WHERE id = ?`, id); err != nil {
			return err
		}
		_, err = s.audit.Append(ctx, tx, audit.Entry{Actor: auditActor(c.Actor), Action: ActionProviderDelete, Object: providerObject(r),
			Change: map[string]any{"kind": r.kind, "identities_unlinked": unlinked}})
		return err
	})
}

// TestResult is the outcome of testing an identity source.
type TestResult struct {
	OK     bool
	Step   string // identity.StepConfig, StepDiscovery or StepClient when it failed
	Detail string // what went wrong, in English, with the client secret masked
}

// TestProvider checks that an identity source works without anyone signing
// in: its provider answers, and accepts the client ID and secret when the
// protocol can tell. The client secret is sent to the provider's own address
// only. Platform administrators only, from the browser; recorded in the audit
// log.
func (s *Service) TestProvider(ctx context.Context, c Caller, id string) (TestResult, error) {
	if err := s.admin(ctx, c, true); err != nil {
		return TestResult{}, err
	}
	if id == LocalProviderID {
		return TestResult{OK: true}, nil
	}
	r, err := provider(ctx, s.db, id)
	if err != nil {
		return TestResult{}, err
	}
	kind, ok := s.kind(r.kind)
	if !ok {
		return TestResult{Step: identity.StepConfig, Detail: "This server was built without the identity source kind " + r.kind + "."}, nil
	}
	cfg, err := s.config(r)
	if err != nil {
		return TestResult{}, err
	}
	res := TestResult{OK: true}
	if err := kind.Test(ctx, cfg, s.callbackURL(r.id)); err != nil {
		res = TestResult{Step: identity.StepDiscovery, Detail: mask(err.Error(), cfg)}
		var te *identity.TestError
		if errors.As(err, &te) {
			res.Step = te.Step
		}
	}
	change := map[string]any{"ok": res.OK}
	if res.Step != "" {
		change["step"] = res.Step
	}
	_, err = s.audit.Record(ctx, audit.Entry{Actor: auditActor(c.Actor), Action: ActionProviderTest, Object: providerObject(r), Change: change})
	return res, err
}

// callbackURL is where an identity source sends the browser back to.
func (s *Service) callbackURL(providerID string) string {
	base := "http://127.0.0.1"
	if s.baseURL != nil {
		base = strings.TrimSuffix(s.baseURL.String(), "/")
	}
	return base + "/api/v1/auth/providers/" + url.PathEscape(providerID) + "/callback"
}

// CallbackURL is the address to register at the identity provider for the
// method with this id.
func (s *Service) CallbackURL(providerID string) string { return s.callbackURL(providerID) }

// mask removes the client secret from text that came from a provider, in
// every form it may have been sent in: as it is, URL-encoded, in base64, and
// as the HTTP basic credentials it travels in to the token endpoint.
func mask(text string, cfg identity.Config) string {
	secret := cfg.ClientSecret
	if len(secret) < 4 {
		return text
	}
	id, _ := cfg.Settings["client_id"].(string)
	forms := []string{
		secret, url.QueryEscape(secret), url.PathEscape(secret),
		base64.StdEncoding.EncodeToString([]byte(secret)), base64.URLEncoding.EncodeToString([]byte(secret)),
		base64.RawStdEncoding.EncodeToString([]byte(secret)), base64.RawURLEncoding.EncodeToString([]byte(secret)),
		base64.StdEncoding.EncodeToString([]byte(url.QueryEscape(id) + ":" + url.QueryEscape(secret))),
		base64.StdEncoding.EncodeToString([]byte(id + ":" + secret)),
	}
	for _, f := range forms {
		if f != "" {
			text = strings.ReplaceAll(text, f, "<client secret>")
		}
	}
	return text
}

// Rewrapper moves every identity source's client secret to the current
// master key, for "keys rotate".
var Rewrapper = masterkey.Rewrapper{Name: "identity_providers", Rewrap: rewrap}

func rewrap(ctx context.Context, tx *dbx.Tx, keys *masterkey.Keyring) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, client_secret FROM identity_providers WHERE client_secret <> '' ORDER BY id`)
	if err != nil {
		return 0, err
	}
	type sealedRow struct{ id, value string }
	var all []sealedRow
	for rows.Next() {
		var r sealedRow
		if err := rows.Scan(&r.id, &r.value); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, r := range all {
		sealed, err := masterkey.Decode(r.value)
		if err != nil {
			return n, fmt.Errorf("%s: %w", r.id, err)
		}
		sealed, changed, err := keys.Rewrap(sealed)
		if err != nil {
			return n, fmt.Errorf("%s: %w", r.id, err)
		}
		if !changed {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE identity_providers SET client_secret = ? WHERE id = ?`, sealed.Encode(), r.id); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package secrets_test

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/hpke"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/ext/secretbackend"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
	"github.com/code2rich/rigdepot/internal/server/masterkey"
	"github.com/code2rich/rigdepot/internal/server/migrations"
	"github.com/code2rich/rigdepot/internal/server/secrets"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

const (
	jdoe   = "0193a4b2-6c1e-7a00-8000-000000000011"
	alex   = "0193a4b2-6c1e-7a00-8000-000000000012"
	laptop = "0193a4b2-6c1e-7a00-8000-000000000201"
	// marker is the value the tests set; it must never be found in the database file.
	marker = "Mk7-secret-marker-QzX4p9-not-a-real-token"
)

var (
	web = access.Actor{UserID: jdoe, Origin: access.OriginWeb}
	cli = access.Actor{UserID: jdoe, Origin: access.OriginCLI, DeviceID: laptop}
)

type fixture struct {
	db   *dbx.DB
	path string
	keys *masterkey.Keyring
	log  *audit.Log
	s    *secrets.Service
}

func setup(t *testing.T, backend secretbackend.Backend) *fixture {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	db := dbxtest.Open(t, "sqlite://"+filepath.ToSlash(path))
	if _, err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, u := range []struct{ id, name string }{{jdoe, "jdoe"}, {alex, "alex"}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO users (id, username, status, created_at, updated_at) VALUES (?, ?, 'active', 1, 1)`, u.id, u.name); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := masterkey.New(bytes.Repeat([]byte{7}, masterkey.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	log, err := audit.New(db, bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{db: db, path: path, keys: keys, log: log, s: secrets.New(db, keys, secrets.Options{Audit: log, Backend: backend})}
}

func deviceKey(t *testing.T) (*ecdh.PrivateKey, secrets.Device) {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k, secrets.Device{ID: laptop, PublicKey: base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes())}
}

func openSealed(t *testing.T, k *ecdh.PrivateKey, s secrets.Sealed) string {
	t.Helper()
	priv, err := hpke.NewDHKEMPrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	r, err := hpke.NewRecipient(s.Enc, priv, hpke.HKDFSHA256(), hpke.AES256GCM(), []byte(s.Info))
	if err != nil {
		t.Fatal(err)
	}
	v, err := r.Open(nil, s.Ciphertext)
	if err != nil {
		t.Fatalf("open %s: %v", s.Name, err)
	}
	return string(v)
}

// The whole path: a value is set, replaced, sealed for a device and opened there with the
// device's private key; it is nowhere in the database file; the audit log says it was replaced.
func TestSetSealOpen(t *testing.T) {
	f := setup(t, nil)
	ctx := context.Background()
	expires := time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC)
	v, err := f.s.Set(ctx, web, "JIRA_API_TOKEN", []byte("first-"+marker), time.Time{})
	if err != nil || v.Version != 1 || !v.ExpiresAt.IsZero() {
		t.Fatalf("set: %+v %v", v, err)
	}
	if v, err = f.s.Set(ctx, cli, "JIRA_API_TOKEN", []byte(marker), expires); err != nil || v.Version != 2 || !v.ExpiresAt.Equal(expires) {
		t.Fatalf("replace: %+v %v", v, err)
	}
	if _, err := f.s.Set(ctx, web, "GITLAB_TOKEN", []byte("glpat-"+marker), time.Time{}); err != nil {
		t.Fatal(err)
	}
	conf, err := f.s.Configured(ctx, jdoe)
	if err != nil || !conf["JIRA_API_TOKEN"] || !conf["GITLAB_TOKEN"] || len(conf) != 2 {
		t.Fatalf("configured: %v %v", conf, err)
	}
	if conf, _ := f.s.Configured(ctx, alex); len(conf) != 0 {
		t.Errorf("someone else's: %v", conf)
	}

	k, dev := deviceKey(t)
	items, missing, err := f.s.SealFor(ctx, cli, dev, []string{"JIRA_API_TOKEN", "FIGMA_TOKEN"})
	if err != nil || len(items) != 1 || len(missing) != 1 || missing[0] != "FIGMA_TOKEN" {
		t.Fatalf("seal: %+v %v %v", items, missing, err)
	}
	it := items[0]
	if it.Info != secrets.Info(jdoe, laptop, "JIRA_API_TOKEN", 2) || it.Version != 2 || !it.ExpiresAt.Equal(expires) {
		t.Errorf("sealed: %+v", it)
	}
	if got := openSealed(t, k, it); got != marker {
		t.Errorf("opened %q", got)
	}
	if bytes.Contains(it.Ciphertext, []byte(marker)) {
		t.Error("the sealed value holds the value")
	}
	// Another info (another person, device, name or version) does not open it.
	priv, _ := hpke.NewDHKEMPrivateKey(k)
	if r, err := hpke.NewRecipient(it.Enc, priv, hpke.HKDFSHA256(), hpke.AES256GCM(), []byte(secrets.Info(alex, laptop, "JIRA_API_TOKEN", 2))); err == nil {
		if _, err := r.Open(nil, it.Ciphertext); err == nil {
			t.Error("opens with someone else's info")
		}
	}
	// All of them when no names are asked for.
	if items, missing, err := f.s.SealFor(ctx, cli, dev, nil); err != nil || len(items) != 2 || len(missing) != 0 {
		t.Errorf("all: %d %v %v", len(items), missing, err)
	}

	// The audit log says it was replaced, not what it is.
	rows, err := f.db.QueryContext(ctx, `SELECT action, change FROM audit_log ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for rows.Next() {
		var a, c string
		rows.Scan(&a, &c)
		actions = append(actions, a)
		if strings.Contains(c, marker) {
			t.Errorf("audit change holds the value: %s", c)
		}
	}
	rows.Close()
	if strings.Join(actions, ",") != "secret.set,secret.set,secret.set" {
		t.Errorf("audit: %v", actions)
	}
	assertNotInFile(t, f)
}

// assertNotInFile closes the database and looks for the marker in its files.
func assertNotInFile(t *testing.T, f *fixture) {
	t.Helper()
	f.db.Close()
	matches, _ := filepath.Glob(f.path + "*")
	if len(matches) == 0 {
		t.Fatal("no database file")
	}
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(marker)) {
			t.Errorf("%s holds the secret's value", filepath.Base(m))
		}
	}
}

func TestSetRefusesWhatItShouldNot(t *testing.T) {
	f := setup(t, nil)
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		a     access.Actor
		key   string
		value string
		want  error
	}{
		{"access token", access.Actor{UserID: jdoe, Origin: access.OriginCLI, TokenID: "t1"}, "JIRA_API_TOKEN", "x", secrets.ErrDenied},
		{"nobody", access.Actor{Origin: access.OriginWeb}, "JIRA_API_TOKEN", "x", secrets.ErrDenied},
		{"the system", access.System, "JIRA_API_TOKEN", "x", secrets.ErrDenied},
		{"lower case", web, "jira_token", "x", secrets.ErrInvalidName},
		{"empty", web, "JIRA_API_TOKEN", "", secrets.ErrInvalidValue},
		{"line break", web, "JIRA_API_TOKEN", "abc\ndef", secrets.ErrInvalidValue},
		{"too long", web, "JIRA_API_TOKEN", strings.Repeat("a", secrets.MaxValue+1), secrets.ErrInvalidValue},
		{"not UTF-8", web, "JIRA_API_TOKEN", "\xff\xfe", secrets.ErrInvalidValue},
	} {
		if _, err := f.s.Set(ctx, tc.a, tc.key, []byte(tc.value), time.Time{}); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	if _, err := f.s.Set(ctx, web, "JIRA_API_TOKEN", []byte(strings.Repeat("a", secrets.MaxValue)), time.Time{}); err != nil {
		t.Errorf("the longest value: %v", err)
	}
}

func TestDelete(t *testing.T) {
	f := setup(t, nil)
	ctx := context.Background()
	if err := f.s.Delete(ctx, web, "JIRA_API_TOKEN"); !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("not set: %v", err)
	}
	f.s.Set(ctx, web, "JIRA_API_TOKEN", []byte(marker), time.Time{})
	if err := f.s.Delete(ctx, access.Actor{UserID: alex, Origin: access.OriginWeb}, "JIRA_API_TOKEN"); !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("someone else's: %v", err)
	}
	if err := f.s.Delete(ctx, web, "JIRA_API_TOKEN"); err != nil {
		t.Fatal(err)
	}
	if conf, _ := f.s.Configured(ctx, jdoe); len(conf) != 0 {
		t.Errorf("still configured: %v", conf)
	}
}

// A record moved to another person, or put back at another version, does not open: the
// additional data binds owner, name and version.
func TestMovedRecordDoesNotOpen(t *testing.T) {
	f := setup(t, nil)
	ctx := context.Background()
	f.s.Set(ctx, web, "JIRA_API_TOKEN", []byte(marker), time.Time{})
	if _, err := f.db.ExecContext(ctx, `INSERT INTO secret_values (user_id, secret_name, ciphertext, nonce, wrapped_key, key_id, version, updated_at)
		SELECT ?, secret_name, ciphertext, nonce, wrapped_key, key_id, version, updated_at FROM secret_values WHERE user_id = ?`, alex, jdoe); err != nil {
		t.Fatal(err)
	}
	_, dev := deviceKey(t)
	if _, _, err := f.s.SealFor(ctx, access.Actor{UserID: alex, Origin: access.OriginCLI, DeviceID: laptop}, dev, nil); err == nil {
		t.Error("a record moved to another person opens")
	}
	if _, err := f.db.ExecContext(ctx, `UPDATE secret_values SET version = 5 WHERE user_id = ?`, jdoe); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.SealFor(ctx, cli, dev, nil); err == nil {
		t.Error("a record at another version opens")
	}
}

func TestSealForRefusesOtherDevicesAndBadKeys(t *testing.T) {
	f := setup(t, nil)
	ctx := context.Background()
	_, dev := deviceKey(t)
	if _, _, err := f.s.SealFor(ctx, web, dev, nil); !errors.Is(err, secrets.ErrDenied) {
		t.Errorf("no device: %v", err)
	}
	other := cli
	other.DeviceID = "0193a4b2-6c1e-7a00-8000-000000000202"
	if _, _, err := f.s.SealFor(ctx, other, dev, nil); !errors.Is(err, secrets.ErrDenied) {
		t.Errorf("another device's key: %v", err)
	}
	if _, _, err := f.s.SealFor(ctx, cli, secrets.Device{ID: laptop, PublicKey: "not-a-key"}, nil); !errors.Is(err, secrets.ErrInvalidKey) {
		t.Errorf("bad key: %v", err)
	}
}

// Rotating the master key moves every value's data key to the new key; the values still open
// and nobody enters them again.
func TestRewrapAfterRotation(t *testing.T) {
	f := setup(t, nil)
	ctx := context.Background()
	f.s.Set(ctx, web, "JIRA_API_TOKEN", []byte(marker), time.Time{})
	old := f.keys.CurrentID()
	rotated, err := f.keys.Rotate(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	counts, err := masterkey.RewrapAll(ctx, f.db, rotated, []masterkey.Rewrapper{secrets.Rewrapper})
	if err != nil || counts["secrets"] != 1 {
		t.Fatalf("rewrap: %v %v", counts, err)
	}
	var keyID string
	f.db.QueryRowContext(ctx, `SELECT key_id FROM secret_values`).Scan(&keyID)
	if keyID == old || keyID != rotated.CurrentID() {
		t.Errorf("key id %s, want %s", keyID, rotated.CurrentID())
	}
	k, dev := deviceKey(t)
	s := secrets.New(f.db, rotated, secrets.Options{Audit: f.log})
	items, _, err := s.SealFor(ctx, cli, dev, nil)
	if err != nil || len(items) != 1 || openSealed(t, k, items[0]) != marker {
		t.Fatalf("after rotation: %v", err)
	}
	// Run again: nothing left to move.
	if counts, err := masterkey.RewrapAll(ctx, f.db, rotated, []masterkey.Rewrapper{secrets.Rewrapper}); err != nil || counts["secrets"] != 0 {
		t.Errorf("second run: %v %v", counts, err)
	}
}

// failing is a backend that cannot be reached.
type failing struct{}

func (failing) ID() string { return "vault" }
func (failing) Wrap(context.Context, []byte, []byte) (secretbackend.Wrapped, error) {
	return secretbackend.Wrapped{}, errors.New("connection refused")
}
func (failing) Unwrap(context.Context, secretbackend.Wrapped, []byte) ([]byte, error) {
	return nil, errors.New("connection refused")
}

// A backend that fails refuses: nothing is stored, and there is no falling back to the
// built-in one.
func TestFailingBackendRefuses(t *testing.T) {
	f := setup(t, failing{})
	ctx := context.Background()
	if _, err := f.s.Set(ctx, web, "JIRA_API_TOKEN", []byte(marker), time.Time{}); err == nil || !strings.Contains(err.Error(), "vault") {
		t.Fatalf("set: %v", err)
	}
	if conf, _ := f.s.Configured(ctx, jdoe); len(conf) != 0 {
		t.Errorf("stored anyway: %v", conf)
	}
	// A value the built-in backend stored does not open through the failing one either.
	secrets.New(f.db, f.keys, secrets.Options{Audit: f.log}).Set(ctx, web, "JIRA_API_TOKEN", []byte(marker), time.Time{})
	_, dev := deviceKey(t)
	if _, _, err := f.s.SealFor(ctx, cli, dev, nil); err == nil {
		t.Error("sealed through a failing backend")
	}
}

func TestDefinitionsAndUse(t *testing.T) {
	f := setup(t, nil)
	ctx := context.Background()
	if err := f.s.Define(ctx, []secrets.Def{{Name: "JIRA_API_TOKEN", Label: "Jira API token", Howto: "Jira, Account settings, Security"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(ctx, `INSERT INTO resources (id, code, kind, origin, scope_kind, policy, audience, status, name, created_at, updated_at)
		VALUES ('r1', 'jira', 'mcp', 'web', 'org', 'optional', 'rd', 'published', 'Jira', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := f.s.UseSecrets(ctx, "r1", []string{"JIRA_API_TOKEN", "JIRA_URL_TOKEN"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.UseSecrets(ctx, "r1", []string{"JIRA_API_TOKEN"}); err != nil {
		t.Fatal(err)
	}
	var n int
	f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM resource_secrets WHERE resource_id = 'r1'`).Scan(&n)
	if n != 1 {
		t.Errorf("resource_secrets: %d rows", n)
	}
	defs, err := f.s.Defs(ctx, []string{"JIRA_API_TOKEN", "JIRA_URL_TOKEN", "NOT_DEFINED"})
	if err != nil || defs["JIRA_API_TOKEN"].Label != "Jira API token" || defs["JIRA_URL_TOKEN"].Label != "" || len(defs) != 2 {
		t.Errorf("defs: %+v %v", defs, err)
	}
	if err := f.s.UseSecrets(ctx, "r1", []string{"lower"}); !errors.Is(err, secrets.ErrInvalidName) {
		t.Errorf("bad name: %v", err)
	}
}

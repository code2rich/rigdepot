// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/hpke"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/logging"
	"github.com/code2rich/rigdepot/internal/repoformat"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/catalog"
	"github.com/code2rich/rigdepot/internal/server/catalog/catalogtest"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
	"github.com/code2rich/rigdepot/internal/server/secrets"
)

// secretMarker is the value set in the tests: it must never come back from the API, nor be in
// the database file or in the log.
const secretMarker = "Zr5-personal-secret-marker-8hQw2-not-a-token"

// lockedBuffer collects the server's log.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// newSecretsAPI is the assembled server with the test kinds, the real device code flow, the
// background parts (the catalog's events reach the secrets) and a log at debug level.
func newSecretsAPI(t *testing.T) (*catalogFixture, *lockedBuffer) {
	t.Helper()
	logs := &lockedBuffer{}
	f := newAPIWith(t, fixtureOptions{realDevices: true, background: true, setup: func(app *App) {
		app.catalogOptions.Kinds, app.catalogOptions.Rules = catalogtest.Kinds(), catalogtest.Rules()
		app.log = logging.New(logs, slog.LevelDebug, logging.JSON)
	}})
	return &catalogFixture{apiFixture: f, t: t}, logs
}

// signInDevice runs the device code flow for user and returns the device token, the device's id
// and its private key.
func (f *catalogFixture) signInDevice(t *testing.T, user string) (string, string, *ecdh.PrivateKey) {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub := base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes())
	rec := f.call("POST", "/api/v1/device/code", "", []byte(`{"name":"dev-laptop","os":"linux","arch":"amd64","cli_version":"0.5.0","public_key":"`+pub+`","key_storage":"file"}`), nil, http.StatusOK)
	dc := decode[httpapi.DeviceCode](t, rec)
	f.call("POST", "/api/v1/device/requests/"+dc.UserCode+"/approve", user, nil, nil, http.StatusNoContent)
	rec = f.call("POST", "/api/v1/device/token", "", []byte(`{"device_code":"`+dc.DeviceCode+`"}`), nil, http.StatusOK)
	tok := decode[httpapi.DeviceToken](t, rec)
	return tok.DeviceToken, tok.DeviceId, k
}

// publishMCP registers and publishes an MCP server that needs JIRA_API_TOKEN, for everyone.
func (f *catalogFixture) publishMCP(t *testing.T) {
	t.Helper()
	body := jsonBody(map[string]any{"kind": "mcp", "meta": map[string]any{"name": "Jira", "when": "Issues"},
		"definition": map[string]any{"server": map[string]any{"type": "http", "url": "https://jira.example.com/mcp",
			"headers": map[string]any{"Authorization": "Bearer ${secret:JIRA_API_TOKEN}"}}, "delivery": "native"}})
	rec := f.call("PUT", "/api/v1/resources/jira/draft", testAdmin, body, nil, http.StatusCreated)
	d := decode[httpapi.Draft](t, rec)
	f.call("POST", "/api/v1/resources/jira/publish", testAdmin, []byte(`{}`), map[string]string{"If-Match": d.Etag}, http.StatusCreated)
}

func TestSecretsAPI(t *testing.T) {
	f, logs := newSecretsAPI(t)
	bearer := func(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

	// Nothing asks for a secret yet: the list is empty and a name no resource uses is refused.
	if l := decode[httpapi.MySecretList](t, f.call("GET", "/api/v1/me/secrets", testMember, nil, nil, http.StatusOK)); len(l.Items) != 0 {
		t.Errorf("before any resource: %+v", l)
	}
	f.call("PUT", "/api/v1/me/secrets/JIRA_API_TOKEN", testMember, jsonBody(map[string]any{"value": secretMarker}), nil, http.StatusNotFound)

	f.publishMCP(t)
	l := decode[httpapi.MySecretList](t, f.call("GET", "/api/v1/me/secrets", testMember, nil, nil, http.StatusOK))
	if len(l.Items) != 1 || l.Items[0].Name != "JIRA_API_TOKEN" || l.Items[0].Configured || len(l.Items[0].UsedBy) != 1 || l.Items[0].UsedBy[0].Code != "jira" {
		t.Fatalf("list: %+v", l)
	}

	// Set in the browser; the answer says it is set and never what it is.
	expires := time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC)
	rec := f.call("PUT", "/api/v1/me/secrets/JIRA_API_TOKEN", testMember, jsonBody(map[string]any{"value": secretMarker, "expires_at": expires}), nil, http.StatusOK)
	got := decode[httpapi.MySecret](t, rec)
	if !got.Configured || got.ExpiresAt == nil || !got.ExpiresAt.Equal(expires) || got.UpdatedAt == nil {
		t.Errorf("set: %s", rec.Body)
	}
	f.call("PUT", "/api/v1/me/secrets/jira_token", testMember, jsonBody(map[string]any{"value": "x"}), nil, http.StatusBadRequest)
	f.call("PUT", "/api/v1/me/secrets/JIRA_API_TOKEN", testMember, jsonBody(map[string]any{"value": "a\nb"}), nil, http.StatusBadRequest)

	// The person's computer: replace from the terminal, then get it sealed to the device's key.
	token, deviceID, key := f.signInDevice(t, testMember)
	f.call("PUT", "/api/v1/me/secrets/JIRA_API_TOKEN", "", jsonBody(map[string]any{"value": secretMarker}), bearer(token), http.StatusOK)
	rec = f.call("POST", "/api/v1/sync/secrets", "", []byte(`{"names":["JIRA_API_TOKEN","FIGMA_TOKEN"]}`), bearer(token), http.StatusOK)
	sealed := decode[httpapi.SealedSecrets](t, rec)
	if sealed.DeviceId != deviceID || sealed.Suite != secrets.Suite || len(sealed.Items) != 1 || len(sealed.Missing) != 1 || sealed.Missing[0] != "FIGMA_TOKEN" {
		t.Fatalf("sealed: %s", rec.Body)
	}
	it := sealed.Items[0]
	if it.Version != 2 || it.Info != secrets.Info(testMember, deviceID, "JIRA_API_TOKEN", 2) {
		t.Errorf("sealed item: %+v", it)
	}
	enc, _ := base64.RawURLEncoding.DecodeString(it.Enc)
	ct, _ := base64.RawURLEncoding.DecodeString(it.Ciphertext)
	priv, _ := hpke.NewDHKEMPrivateKey(key)
	r, err := hpke.NewRecipient(enc, priv, hpke.HKDFSHA256(), hpke.AES256GCM(), []byte(it.Info))
	if err != nil {
		t.Fatal(err)
	}
	if v, err := r.Open(nil, ct); err != nil || string(v) != secretMarker {
		t.Fatalf("open on the device: %q %v", v, err)
	}
	// Only device tokens get sealed values: not the browser.
	f.call("POST", "/api/v1/sync/secrets", testMember, []byte(`{}`), nil, http.StatusForbidden)

	// The administrator sees that it is set for nobody but themself.
	if l := decode[httpapi.MySecretList](t, f.call("GET", "/api/v1/me/secrets", testAdmin, nil, nil, http.StatusOK)); len(l.Items) != 1 || l.Items[0].Configured {
		t.Errorf("the administrator's list: %+v", l)
	}

	// The catalog says it is configured for the member (hooks.secretsConfigured).
	if c := f.call("GET", "/api/v1/catalog/jira", testMember, nil, nil, http.StatusOK); !strings.Contains(c.Body.String(), `"configured":true`) {
		t.Errorf("catalog: %s", c.Body)
	}

	// Delete: gone from the list's state, the second time 404.
	f.call("DELETE", "/api/v1/me/secrets/JIRA_API_TOKEN", testMember, nil, nil, http.StatusNoContent)
	f.call("DELETE", "/api/v1/me/secrets/JIRA_API_TOKEN", testMember, nil, nil, http.StatusNotFound)
	if l := decode[httpapi.MySecretList](t, f.call("GET", "/api/v1/me/secrets", testMember, nil, nil, http.StatusOK)); l.Items[0].Configured {
		t.Errorf("after delete: %+v", l)
	}

	// The audit log records the replacements, not the value.
	audit := f.call("GET", "/api/v1/audit", testAdmin, nil, nil, http.StatusOK)
	for _, want := range []string{"secret.set", "secret.delete"} {
		if !strings.Contains(audit.Body.String(), want) {
			t.Errorf("audit lacks %s", want)
		}
	}

	// The resource's use of the secret was recorded when it was published.
	waitFor(t, func() bool {
		var n int
		f.app.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM resource_secrets`).Scan(&n)
		return n == 1
	})

	// No answer, no file of the database and no log line holds the value.
	for _, body := range []string{audit.Body.String(), rec.Body.String()} {
		if strings.Contains(body, secretMarker) {
			t.Error("an answer holds the value")
		}
	}
	if strings.Contains(logs.String(), secretMarker) {
		t.Error("the log holds the value")
	}
	if !strings.Contains(logs.String(), "/api/v1/me/secrets") {
		t.Error("the requests were not logged at all, so the check above proves nothing")
	}
	f.app.db.Close()
	dir := f.app.cfg.DataDir
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(files) == 0 {
		t.Fatal("no database files")
	}
	for _, p := range files {
		if data, err := os.ReadFile(p); err == nil && bytes.Contains(data, []byte(secretMarker)) {
			t.Errorf("%s holds the value", filepath.Base(p))
		}
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for range 200 {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}

// A repository imported through a source defines its secrets in the secrets domain: the label
// and where to get one from rigdepot.yaml reach the person's list of secrets.
func TestImportedRepositoryDefinesSecrets(t *testing.T) {
	f, _ := newSecretsAPI(t)
	ctx := context.Background()
	source := dbx.NewID()
	if _, err := f.app.db.ExecContext(ctx, `INSERT INTO sources (id, name, url, created_at, updated_at) VALUES (?, 'resources', 'https://git.example.com/r.git', 1, 1)`, source); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "rigdepot.yaml"), []byte("schema: 1\nsecrets:\n  JIRA_API_TOKEN: {label: Jira API token, howto: \"Jira, Account settings, Security\"}\n"), 0o644)
	repo, err := repoformat.Read(os.DirFS(dir), repoformat.Options{Kinds: catalogtest.Kinds()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.catalog.ImportRepo(ctx, access.System, catalog.ImportSource{SourceID: source, Commit: "c1"}, repo); err != nil {
		t.Fatal(err)
	}
	f.publishMCP(t)
	l := decode[httpapi.MySecretList](t, f.call("GET", "/api/v1/me/secrets", testMember, nil, nil, http.StatusOK))
	if len(l.Items) != 1 || l.Items[0].Label != "Jira API token" || l.Items[0].Howto == nil || *l.Items[0].Howto != "Jira, Account settings, Security" {
		t.Errorf("list: %+v", l)
	}
}

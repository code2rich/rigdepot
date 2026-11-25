// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/logging"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/config"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
	"github.com/code2rich/rigdepot/internal/server/masterkey"
	"github.com/code2rich/rigdepot/internal/server/migrations"
	"github.com/code2rich/rigdepot/internal/server/settings"
)

const (
	testAdmin  = "0193a4b2-6c1e-7a00-8000-000000000001"
	testMember = "0193a4b2-6c1e-7a00-8000-000000000011"
	testUser   = "X-Test-User" // the header the test's stand-in for sign-in reads
)

// fakeFirstAdmin stands in for authn (M1-20), which owns passwords and
// sessions: it makes the users row a platform administrator and "signs in"
// with a placeholder cookie.
type fakeFirstAdmin struct{}

func (fakeFirstAdmin) CreateFirstAdmin(ctx context.Context, tx *dbx.Tx, req httpapi.SetupAdminRequest) (settings.NewAdmin, error) {
	if len(req.Password) < 12 {
		return settings.NewAdmin{}, httpapi.NewProblem(http.StatusBadRequest, httpapi.CodeRequestInvalid, "password too short")
	}
	// newAPI has the row already, for the tests that skip the wizard.
	_, err := tx.ExecContext(ctx, `UPDATE users SET username = ?, is_admin = TRUE WHERE id = ?`, req.Username, testAdmin)
	return settings.NewAdmin{UserID: testAdmin, Username: req.Username}, err
}

func (fakeFirstAdmin) SignIn(_ context.Context, userID string) (string, httpapi.Me, error) {
	return "session=<placeholder>; Path=/; HttpOnly", httpapi.Me{Id: userID, Username: "admin", Status: "active", PlatformAdmin: true}, nil
}

type apiFixture struct {
	app *App
	h   http.Handler
}

// newAPI assembles the server around a fresh database like serve does, with
// the real access package, an administrator and a member, and a sign-in
// stand-in that takes the caller from a header. withHooks adds the
// stand-in for creating the first administrator.
func newAPI(t *testing.T, withHooks bool) *apiFixture {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Load(config.Options{LookupEnv: func(k string) (string, bool) {
		switch k {
		case config.EnvName("BASE_URL"):
			return "http://127.0.0.1:8080", true
		case config.EnvName("DATA_DIR"):
			return dir, true
		}
		return "", false
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db := dbxtest.Open(t, "sqlite://"+filepath.ToSlash(filepath.Join(dir, "test.db")))
	if _, err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, u := range []struct {
		id, name string
		admin    bool
	}{{testAdmin, "admin", true}, {testMember, "jdoe", false}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO users (id, username, display_name, status, is_admin, created_at, updated_at) VALUES (?, ?, ?, 'active', ?, 1, 1)`,
			u.id, u.name, "Name of "+u.name, u.admin); err != nil {
			t.Fatal(err)
		}
	}
	app := NewApp(cfg, logging.Discard(), db, Mounts{})
	if withHooks {
		app.hooks.firstAdmin = fakeFirstAdmin{}
	}
	if err := app.openServices(ctx); err != nil {
		t.Fatal(err)
	}
	app.mounts = apiMounts(app)
	api := app.mounts.API
	app.mounts.API = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u := r.Header.Get(testUser); u != "" {
			r = r.WithContext(access.WithActor(r.Context(), access.Actor{UserID: u, Origin: access.OriginWeb}))
		}
		api.ServeHTTP(w, r)
	})
	return &apiFixture{app: app, h: app.Handler()}
}

func (f *apiFixture) do(t *testing.T, method, path, user, body string, header map[string]string) (*httptest.ResponseRecorder, httpapi.Problem) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if user != "" {
		req.Header.Set(testUser, user)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	var p httpapi.Problem
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/problem+json") {
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatalf("%s %s: problem does not decode: %v", method, path, err)
		}
	}
	return rec, p
}

func TestUnimplementedOperationsAnswer501(t *testing.T) {
	f := newAPI(t, false)
	rec, p := f.do(t, http.MethodGet, "/api/v1/meta", "", "", nil)
	if rec.Code != http.StatusNotImplemented || p.Code != httpapi.CodeNotImplemented || p.RequestId == nil || *p.RequestId != rec.Header().Get(RequestIDHeader) {
		t.Errorf("GET /api/v1/meta: %d %+v", rec.Code, p)
	}
	if rec, p := f.do(t, http.MethodGet, "/api/v1/no-such-thing", "", "", nil); rec.Code != http.StatusNotFound || p.Code != "not_found" {
		t.Errorf("unknown path: %d %+v", rec.Code, p)
	}
}

func TestSettingsAPI(t *testing.T) {
	f := newAPI(t, true)
	if rec, p := f.do(t, http.MethodGet, "/api/v1/settings", "", "", nil); rec.Code != http.StatusUnauthorized || p.Code != "auth.required" {
		t.Errorf("signed out: %d %+v", rec.Code, p)
	}
	if rec, p := f.do(t, http.MethodGet, "/api/v1/settings", testMember, "", nil); rec.Code != http.StatusForbidden || p.Code != "access.denied" {
		t.Errorf("member: %d %+v", rec.Code, p)
	}
	rec, _ := f.do(t, http.MethodGet, "/api/v1/settings", testAdmin, "", nil)
	var got httpapi.PlatformSettings
	json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != http.StatusOK || got.Etag == "" || rec.Header().Get("ETag") != got.Etag || got.Mail.HasPassword {
		t.Fatalf("admin: %d %s", rec.Code, rec.Body)
	}

	const pw = "Zq8-mail-password-marker-4Kd9"
	change := `{"organization":{"name":"Example Corp"},"mail":{"enabled":true,"host":"smtp.example.com","port":465,"username":"rigdepot@example.com","password":"` + pw + `","from":"rigdepot@example.com"}}`
	if rec, p := f.do(t, http.MethodPatch, "/api/v1/settings", testAdmin, change, nil); rec.Code != http.StatusPreconditionRequired || p.Code != "precondition.required" {
		t.Errorf("no If-Match: %d %+v", rec.Code, p)
	}
	if rec, p := f.do(t, http.MethodPatch, "/api/v1/settings", testAdmin, change, map[string]string{"If-Match": `"settings-0"`}); rec.Code != http.StatusPreconditionFailed || p.Code != "precondition.failed" {
		t.Errorf("stale If-Match: %d %+v", rec.Code, p)
	}
	bad := `{"mail":{"enabled":true}}`
	if rec, p := f.do(t, http.MethodPatch, "/api/v1/settings", testAdmin, bad, map[string]string{"If-Match": got.Etag}); rec.Code != http.StatusBadRequest ||
		p.Errors == nil || (*p.Errors)[0].Field != "mail.host" {
		t.Errorf("invalid: %d %+v", rec.Code, p)
	}
	rec, _ = f.do(t, http.MethodPatch, "/api/v1/settings", testAdmin, change, map[string]string{"If-Match": got.Etag})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), pw) || !strings.Contains(rec.Body.String(), `"has_password":true`) ||
		!strings.Contains(rec.Body.String(), `"security":"tls"`) {
		t.Fatalf("change: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := f.do(t, http.MethodGet, "/api/v1/settings", testAdmin, "", nil); strings.Contains(rec.Body.String(), pw) {
		t.Error("GET returns the password")
	}

	// The change is in the audit log, the password as "replaced".
	rec, _ = f.do(t, http.MethodGet, "/api/v1/audit?action=settings.", testAdmin, "", nil)
	var page httpapi.AuditPage
	json.Unmarshal(rec.Body.Bytes(), &page)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), pw) {
		t.Fatalf("audit: %d %s", rec.Code, rec.Body)
	}
	var update *httpapi.AuditEntry
	for i, e := range page.Items {
		if e.Action == settings.ActionSettingsUpdate && e.Result == httpapi.AuditResultOk {
			update = &page.Items[i]
		}
	}
	if update == nil || update.Change["mail.password"] != "replaced" || update.Actor == nil || update.Actor.Username != "admin" {
		t.Errorf("audit entries: %s", rec.Body)
	}
	// The member's refused read is there too.
	if !strings.Contains(rec.Body.String(), `"result":"denied"`) {
		t.Errorf("the refusal is not in the audit log: %s", rec.Body)
	}
}

func TestSettingsChangeOnlyInTheBrowser(t *testing.T) {
	f := newAPI(t, true)
	cli := f.app.mounts.API
	f.app.mounts.API = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cli.ServeHTTP(w, r.WithContext(access.WithActor(r.Context(), access.Actor{UserID: testAdmin, Origin: access.OriginCLI, ViaTool: "claude-code"})))
	})
	f.h = f.app.Handler()
	rec, p := f.do(t, http.MethodPatch, "/api/v1/settings", "", `{"organization":{"name":"x"}}`, map[string]string{"If-Match": `"x"`})
	if rec.Code != http.StatusForbidden || p.Code != "access.denied" {
		t.Errorf("from the command line: %d %+v", rec.Code, p)
	}
}

func TestSetupAPI(t *testing.T) {
	f := newAPI(t, true)
	ctx := context.Background()
	token, _, err := f.app.settings.SetupToken(ctx)
	if err != nil || token == "" {
		t.Fatal(token, err)
	}
	rec, _ := f.do(t, http.MethodGet, "/api/v1/setup", "", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"admin_created":false`) {
		t.Errorf("status: %d %s", rec.Code, rec.Body)
	}
	body := `{"username":"admin","password":"<placeholder: at least 12>"}`
	if rec, p := f.do(t, http.MethodPost, "/api/v1/setup/admin", "", body, map[string]string{"X-Setup-Token": "wrong"}); rec.Code != http.StatusForbidden || p.Code != "setup.token_invalid" {
		t.Errorf("wrong token: %d %+v", rec.Code, p)
	}
	if rec, p := f.do(t, http.MethodPost, "/api/v1/setup/admin", "", `{"username":"admin","password":"short"}`, map[string]string{"X-Setup-Token": token}); rec.Code != http.StatusBadRequest {
		t.Errorf("weak password: %d %+v", rec.Code, p)
	}
	rec, _ = f.do(t, http.MethodPost, "/api/v1/setup/admin", "", body, map[string]string{"X-Setup-Token": token})
	if rec.Code != http.StatusCreated || !strings.HasPrefix(rec.Header().Get("Set-Cookie"), "session=") || !strings.Contains(rec.Body.String(), `"platform_admin":true`) {
		t.Fatalf("create: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	if rec, p := f.do(t, http.MethodPost, "/api/v1/setup/admin", "", body, map[string]string{"X-Setup-Token": token}); rec.Code != http.StatusConflict || p.Code != "setup.admin_exists" {
		t.Errorf("again: %d %+v", rec.Code, p)
	}
	if rec, p := f.do(t, http.MethodPost, "/api/v1/setup/complete", "", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("complete signed out: %d %+v", rec.Code, p)
	}
	if rec, p := f.do(t, http.MethodPost, "/api/v1/setup/complete", testMember, "", nil); rec.Code != http.StatusForbidden {
		t.Errorf("complete by a member: %d %+v", rec.Code, p)
	}
	if rec, _ := f.do(t, http.MethodPost, "/api/v1/setup/complete", testAdmin, "", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("complete: %d %s", rec.Code, rec.Body)
	}

	// The acceptance check "once the wizard is complete, its operations are
	// closed".
	for _, op := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/setup", ""},
		{http.MethodPost, "/api/v1/setup/admin", body},
		{http.MethodPost, "/api/v1/setup/complete", ""},
	} {
		rec, p := f.do(t, op.method, op.path, testAdmin, op.body, map[string]string{"X-Setup-Token": token})
		if rec.Code != http.StatusNotFound || p.Code != "setup.closed" {
			t.Errorf("%s %s after completion: %d %+v", op.method, op.path, rec.Code, p)
		}
	}
}

func TestSetupWithoutAccountsYetIs501(t *testing.T) {
	f := newAPI(t, false)
	token, _, _ := f.app.settings.SetupToken(context.Background())
	rec, p := f.do(t, http.MethodPost, "/api/v1/setup/admin", "", `{"username":"admin","password":"<placeholder: at least 12>"}`, map[string]string{"X-Setup-Token": token})
	if rec.Code != http.StatusNotImplemented || p.Code != httpapi.CodeNotImplemented {
		t.Errorf("%d %+v", rec.Code, p)
	}
}

func TestAuditAPI(t *testing.T) {
	f := newAPI(t, true)
	ctx := context.Background()
	for _, e := range []audit.Entry{
		{Actor: audit.Actor{UserID: testMember, Origin: audit.OriginCLI, ViaTool: "claude-code", DeviceID: "0193a4b2-6c1e-7a00-8000-000000000201"},
			Action: "resource.publish", Object: audit.Object{Kind: "resource", ID: "task-dev", Label: "=cmd|' /C calc'!A0"}},
		{Actor: audit.Actor{UserID: testAdmin, Origin: audit.OriginWeb}, Action: "product.member.add", Object: audit.Object{Kind: "user", ID: testMember, Label: "jdoe"}},
		{Actor: audit.Actor{UserID: testAdmin, Origin: audit.OriginWeb}, Action: "resource.disable", Object: audit.Object{Kind: "resource", ID: "other", Label: "Other"}},
	} {
		if _, err := f.app.audit.Record(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	list := func(user, query string) (int, httpapi.AuditPage) {
		rec, _ := f.do(t, http.MethodGet, "/api/v1/audit"+query, user, "", nil)
		var page httpapi.AuditPage
		json.Unmarshal(rec.Body.Bytes(), &page)
		return rec.Code, page
	}
	if code, _ := list("", ""); code != http.StatusUnauthorized {
		t.Errorf("signed out: %d", code)
	}
	if code, page := list(testAdmin, ""); code != http.StatusOK || len(page.Items) != 3 || page.Items[0].Seq != 3 {
		t.Errorf("admin sees all: %d %+v", code, page)
	}
	// A member sees what they did and what was done to them, whatever they
	// ask for.
	if code, page := list(testMember, ""); code != http.StatusOK || len(page.Items) != 2 {
		t.Errorf("member: %d %+v", code, page)
	}
	if _, page := list(testMember, "?object_kind=resource&object_id=other"); len(page.Items) != 0 {
		t.Errorf("member asking for someone else's resource: %+v", page)
	}
	_, page := list(testMember, "?actor=me")
	if len(page.Items) != 1 || page.Items[0].ViaTool == nil || *page.Items[0].ViaTool != "claude-code" || page.Items[0].Device == nil {
		t.Errorf("actor=me: %+v", page)
	}
	code, page := list(testAdmin, "?limit=2")
	if code != http.StatusOK || len(page.Items) != 2 || page.NextCursor == nil {
		t.Fatalf("first page: %d %+v", code, page)
	}
	if _, next := list(testAdmin, "?limit=2&cursor="+*page.NextCursor); len(next.Items) != 1 || next.Items[0].Seq != 1 || next.NextCursor != nil {
		t.Errorf("second page: %+v", next)
	}
	for _, q := range []string{"?cursor=garbage", "?limit=0", "?limit=500"} {
		if rec, p := f.do(t, http.MethodGet, "/api/v1/audit"+q, testAdmin, "", nil); rec.Code != http.StatusBadRequest || p.Code != httpapi.CodeRequestInvalid {
			t.Errorf("%s: %d %+v", q, rec.Code, p)
		}
	}

	rec, _ := f.do(t, http.MethodGet, "/api/v1/audit/export?action=resource.", testAdmin, "", nil)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("export: %d %v", rec.Code, rec.Header())
	}
	records, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil || len(records) != 3 {
		t.Fatalf("export: %q %v", records, err)
	}
	if records[1][10] != "'=cmd|' /C calc'!A0" {
		t.Errorf("a formula label was not defused: %q", records[1][10])
	}
	// The export is audited.
	if _, page := list(testAdmin, "?action=audit.export"); len(page.Items) != 1 || page.Items[0].Change["action"] != "resource." {
		t.Errorf("export entry: %+v", page)
	}
}

// TestServeSetsUpKeysAndSetup starts the real server twice on one data
// directory: the first start generates the master key and the setup code
// and prints them; the second reads the key and prints the same code.
func TestServeSetsUpKeysAndSetup(t *testing.T) {
	e, _, _ := testEnv(t, nil)
	dataDir := e.lookupEnvValue(config.EnvName("DATA_DIR"))
	srv := startServe(t, e)
	srv.wait(t)
	first := srv.stdout.String()
	code := setupCode(t, first)
	if !strings.Contains(first, "generated a new master key") {
		t.Errorf("first start does not say it generated the key:\n%s", first)
	}
	if info, err := statKey(dataDir); err != nil || (info != 0o600 && info != 0) {
		t.Errorf("master.key: mode %v, %v", info, err)
	}

	e2, _, _ := testEnv(t, map[string]string{config.EnvName("DATA_DIR"): dataDir})
	srv = startServe(t, e2)
	srv.wait(t)
	second := srv.stdout.String()
	if setupCode(t, second) != code || !strings.Contains(second, `"msg":"master key loaded"`) || strings.Contains(second, "generated a new master key") {
		t.Errorf("second start:\n%s", second)
	}
}

func setupCode(t *testing.T, logs string) string {
	t.Helper()
	for _, line := range strings.Split(logs, "\n") {
		var rec struct {
			Code string `json:"setup_code"`
		}
		if json.Unmarshal([]byte(line), &rec) == nil && rec.Code != "" {
			return rec.Code
		}
	}
	t.Fatalf("no setup code in the log:\n%s", logs)
	return ""
}

// TestMissingKeyFileIsNotReplaced: once the database holds sealed data, a
// lost key file stops the server instead of being silently regenerated.
func TestMissingKeyFileIsNotReplaced(t *testing.T) {
	e, _, stderr := testEnv(t, nil)
	dataDir := e.lookupEnvValue(config.EnvName("DATA_DIR"))
	startServe(t, e).wait(t)
	if err := removeKey(dataDir); err != nil {
		t.Fatal(err)
	}
	e2, stdout, _ := testEnv(t, map[string]string{config.EnvName("DATA_DIR"): dataDir})
	if code := runBounded(e2, "serve"); code != exitFailure || !strings.Contains(stdout.String(), "restore the key file") {
		t.Errorf("exit %d\n%s%s", code, stdout, stderr)
	}
	if _, err := statKey(dataDir); err == nil {
		t.Error("a new key file was generated")
	}
}

func TestKeysRotateAndAuditVerifyCommands(t *testing.T) {
	e, _, _ := testEnv(t, nil)
	dataDir := e.lookupEnvValue(config.EnvName("DATA_DIR"))
	srv := startServe(t, e)
	srv.wait(t)
	code := setupCode(t, srv.stdout.String())
	reenv := func() (*env, *syncBuffer, *syncBuffer) {
		return testEnv(t, map[string]string{config.EnvName("DATA_DIR"): dataDir})
	}

	e1, out, errOut := reenv()
	if c := runBounded(e1, "audit", "verify"); c != exitOK || !strings.Contains(out.String(), "intact: 0 entries") {
		t.Errorf("verify an empty log: %d %s %s", c, out, errOut)
	}
	e2, out, errOut := reenv()
	if c := runBounded(e2, "keys", "rotate"); c != exitOK || !strings.Contains(out.String(), "Rewrapped 2 sealed records (settings 2)") {
		t.Fatalf("rotate: %d %s %s", c, out, errOut)
	}
	e3, out, errOut := reenv()
	if c := runBounded(e3, "audit", "verify"); c != exitOK || !strings.Contains(out.String(), "intact: 1 entries") {
		t.Errorf("verify after rotating (the rotation's own entry): %d %s %s", c, out, errOut)
	}
	// The server starts with the rotated key and still knows its setup code.
	e4, _, _ := reenv()
	srv = startServe(t, e4)
	resp, body := get(t, "http://"+srv.addr+"/readyz")
	srv.wait(t)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"master_key":"ok"`) || setupCode(t, srv.stdout.String()) != code {
		t.Errorf("after rotation: %s %s\n%s", resp.Status, body, srv.stdout)
	}

	// Someone with the database changes an entry: verify says so.
	ctx := context.Background()
	db := dbxtest.Open(t, "sqlite://"+filepath.ToSlash(filepath.Join(dataDir, brand.CLILong+".db")))
	for _, s := range []string{`DROP TRIGGER audit_log_no_update`, `UPDATE audit_log SET object_label = 'nothing happened' WHERE seq = 1`} {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	e5, _, errOut := reenv()
	if c := runBounded(e5, "audit", "verify"); c != exitFailure || !strings.Contains(errOut.String(), "broken at #1") {
		t.Errorf("verify after tampering: %d %s", c, errOut)
	}
}

func TestKeysRotateRefusesAnotherKey(t *testing.T) {
	e, _, _ := testEnv(t, nil)
	dataDir := e.lookupEnvValue(config.EnvName("DATA_DIR"))
	startServe(t, e).wait(t)
	other := filepath.Join(t.TempDir(), "other.key")
	if err := writeKeyFile(other); err != nil {
		t.Fatal(err)
	}
	e2, _, errOut := testEnv(t, map[string]string{config.EnvName("DATA_DIR"): dataDir, config.EnvName("MASTER_KEY_FILE"): other})
	if c := runBounded(e2, "keys", "rotate"); c != exitFailure || !strings.Contains(errOut.String(), "master key does not open") {
		t.Errorf("rotate with another key: %d %s", c, errOut)
	}
	if ids := keyLines(t, other); ids != 1 {
		t.Errorf("the wrong key file was rotated: %d keys", ids)
	}
}

func keyFile(dataDir string) string { return filepath.Join(dataDir, "master.key") }

// statKey returns the key file's permission bits (0 on Windows, which has
// none to check).
func statKey(dataDir string) (os.FileMode, error) {
	info, err := os.Stat(keyFile(dataDir))
	if err != nil {
		return 0, err
	}
	if runtime.GOOS == "windows" {
		return 0, nil
	}
	return info.Mode().Perm(), nil
}

func removeKey(dataDir string) error { return os.Remove(keyFile(dataDir)) }

func writeKeyFile(path string) error {
	_, err := masterkey.Load(masterkey.LoadOptions{File: path, Generate: true})
	return err
}

func keyLines(t *testing.T, path string) int {
	t.Helper()
	k, err := masterkey.Load(masterkey.LoadOptions{File: path})
	if err != nil {
		t.Fatal(err)
	}
	return len(k.IDs())
}

func TestSendTestMail(t *testing.T) {
	f := newAPI(t, false)
	body := `{"to":"admin@example.com"}`
	if rec, p := f.do(t, http.MethodPost, "/api/v1/settings/mail/test", testAdmin, body, nil); rec.Code != http.StatusNotImplemented {
		t.Errorf("without a mail channel: %d %+v", rec.Code, p)
	}

	f = newAPI(t, true)
	const pw = "Zq8-mail-password-marker-4Kd9"
	var sentWith settings.MailConfig
	f.app.hooks.sendTestMail = func(_ context.Context, server settings.MailConfig, to string) error {
		sentWith = server
		return errors.New("smtp.example.com:465: 535 authentication failed for rigdepot@example.com with " + pw)
	}
	rec, _ := f.do(t, http.MethodGet, "/api/v1/settings", testAdmin, "", nil)
	change := `{"mail":{"enabled":true,"host":"smtp.example.com","port":465,"username":"rigdepot@example.com","password":"` + pw + `","from":"rigdepot@example.com"}}`
	if rec, _ := f.do(t, http.MethodPatch, "/api/v1/settings", testAdmin, change, map[string]string{"If-Match": rec.Header().Get("ETag")}); rec.Code != http.StatusOK {
		t.Fatalf("change: %d %s", rec.Code, rec.Body)
	}
	if rec, p := f.do(t, http.MethodPost, "/api/v1/settings/mail/test", testMember, body, nil); rec.Code != http.StatusForbidden {
		t.Errorf("a member: %d %+v", rec.Code, p)
	}
	rec, p := f.do(t, http.MethodPost, "/api/v1/settings/mail/test", testAdmin, body, nil)
	if rec.Code != http.StatusBadGateway || p.Code != "mail.failed" || strings.Contains(rec.Body.String(), pw) || !strings.Contains(*p.Detail, "535 authentication failed") {
		t.Errorf("failed test mail: %d %s", rec.Code, rec.Body)
	}
	if sentWith.Password != pw || sentWith.Host != "smtp.example.com" {
		t.Errorf("the channel got %+v", sentWith.Mail)
	}
}

// TestAccessIsMountedAndAudited: access's operations answer through the
// composed API, and its writes go into the audit chain in their own
// transaction, with access's action names.
func TestAccessIsMountedAndAudited(t *testing.T) {
	f := newAPI(t, false)
	rec, _ := f.do(t, http.MethodGet, "/api/v1/me", testMember, "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"username":"jdoe"`) {
		t.Fatalf("GET /api/v1/me: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := f.do(t, http.MethodGet, "/api/v1/me", "", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("signed out: %d", rec.Code)
	}

	ctx := context.Background()
	admin := access.Actor{UserID: testAdmin, Origin: access.OriginCLI, ViaTool: "claude-code", DeviceID: "0193a4b2-6c1e-7a00-8000-000000000201"}
	if _, err := f.app.access.CreateProduct(ctx, admin, access.NewProduct{Code: "shop", Name: "Shop", Owners: []string{testMember}}); err != nil {
		t.Fatal(err)
	}
	member := access.Actor{UserID: testMember, Origin: access.OriginWeb}
	if _, err := f.app.access.CreateProduct(ctx, member, access.NewProduct{Code: "mine", Name: "Mine", Owners: []string{testMember}}); err == nil {
		t.Fatal("a member created a product")
	}
	page, err := f.app.audit.Query(ctx, audit.Filter{Action: string(access.ActionProductCreate)}, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 2 {
		t.Fatalf("product.create entries: %+v", page.Rows)
	}
	denied, ok := page.Rows[0], page.Rows[1]
	if ok.Result != audit.ResultOK || ok.Category != audit.CategoryPlatform || ok.Actor != auditActor(admin) || ok.Object.Kind != "product" {
		t.Errorf("allowed entry %+v", ok)
	}
	if denied.Result != audit.ResultDenied || denied.Actor.UserID != testMember || denied.Reason == "" {
		t.Errorf("denied entry %+v", denied)
	}
	if rep, err := f.app.audit.Verify(ctx, 1, ""); err != nil || rep.Broken != nil || rep.Checked < 2 {
		t.Errorf("chain: %+v %v", rep, err)
	}
}

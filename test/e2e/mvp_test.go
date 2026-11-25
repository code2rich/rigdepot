// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package e2e

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/tailscale/hujson"

	"github.com/code2rich/rigdepot/ext/identity/identitytest"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/testenv"
	"github.com/code2rich/rigdepot/test/internal/binary"
)

// mvpSecret is the member's personal secret in TestMVPPath. It may reach the MCP server's
// environment and nothing else: no file in either home directory, no answer of the server.
const mvpSecret = "Zr4-mvp-personal-secret-marker-7Qk2"

// fakeMCPDump names the variable that makes the test binary act as a stdio MCP server (see
// TestMain): it writes the environment it was started with to the file the variable names.
const fakeMCPDump = "E2E_FAKE_MCP_DUMP"

// TestMVPPath is the MVP criterion of docs/design.md as one test, row by row of the table in
// docs/architecture/11-testing.md ("端到端：MVP 路径就是一个测试"), with the real rgd-server and the
// real rgd:
//
//   - deploy the server: the first administrator through the setup wizard, a sign-in method
//     (OpenID Connect against a fake provider on this machine, besides local accounts), the
//     wizard closed;
//   - a product and its owner; a member invited to a local account, another who signs in through
//     the provider;
//   - connect the resource repository: a git server over HTTPS on this machine, imported, the
//     check stopping a resource with a token in plain text, the rest published;
//   - everyone connects with one command: rgd login on two computers, approved in the browser;
//   - the same skills and MCP in two tools: what rgd sync --plan shows is what rgd sync writes,
//     into Claude Code and Codex;
//   - personal secrets stay out of the configuration: set with rgd secret set, absent from every
//     file of both homes, present in the MCP server's environment through rgd mcp run and the
//     single entry;
//   - the administrator sees who connected; a resource that could not be installed in one tool is
//     reported to the server;
//   - feedback from the command line, handled by the product's owner and rated;
//   - the audit log has the whole way and its chain is intact;
//   - everyone uninstalls with one command, with the server gone, back to every byte.
//
// Each step checks both sides: what the server answers and what is on the computers.
func TestMVPPath(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real programs")
	}
	root := filepath.Join(testenv.Current(t).Home, t.Name())

	// Two people's computers, each with Claude Code, Codex and OpenCode set up the way the
	// fixture has them. The member already has a skill of their own where Codex would get
	// unit-testing: that resource cannot be installed in that tool.
	owner := newWorldNamed(t, "owner")
	member := newWorldNamed(t, "member")
	own := filepath.Join(member.home, ".agents", "skills", brand.Prefix+"unit-testing")
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(own, "SKILL.md"), []byte("---\nname: my-unit-testing\ndescription: Mine.\n---\n\nMine.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ownerBefore, memberBefore := snapshot(t, owner.home), snapshot(t, member.home)

	// The organisation's resource repository, on a git server over HTTPS; the sign-in provider.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dump := filepath.Join(root, "mcp-env.txt")
	repoURL, ca := mvpRepository(t, root, self, dump)
	idp := identitytest.NewOIDC(t, "/realms/dev")

	// ---- Deploy the server.
	srv := startServer(t, root, brand.EnvPrefix+"GIT="+testenv.NeedGit(t), "GIT_SSL_CAINFO="+ca)
	anon := srv.newBrowser(t)
	var setup struct {
		AdminCreated bool `json:"admin_created"`
	}
	anon.call(http.MethodGet, "/api/v1/setup", nil, http.StatusOK, &setup)
	if setup.AdminCreated {
		t.Fatal("a new server already has an administrator")
	}
	admin := srv.firstAdmin(t)
	anon.call(http.MethodGet, "/api/v1/setup", nil, http.StatusOK, &setup)
	if !setup.AdminCreated {
		t.Fatal("the wizard does not know the administrator was created")
	}

	// Sign-in methods: local accounts are there and on; the organisation's OIDC provider is
	// added with its client secret and tested.
	var providers struct {
		Items []struct {
			ID, Kind string
			Enabled  bool
		}
	}
	admin.call(http.MethodGet, "/api/v1/identity-providers", nil, http.StatusOK, &providers)
	if len(providers.Items) != 1 || providers.Items[0].ID != "local" || !providers.Items[0].Enabled {
		t.Fatalf("sign-in methods of a new server: %+v", providers.Items)
	}
	var sso struct {
		ID              string `json:"id"`
		HasClientSecret bool   `json:"has_client_secret"`
	}
	admin.call(http.MethodPost, "/api/v1/identity-providers", map[string]any{
		"kind": "oidc", "name": "Company SSO", "enabled": true,
		"config": map[string]any{"issuer": idp.URL, "client_id": idp.ClientID}, "client_secret": idp.ClientSecret,
	}, http.StatusCreated, &sso)
	if !sso.HasClientSecret {
		t.Errorf("the provider has no client secret: %+v", sso)
	}
	var tested struct{ OK bool }
	admin.call(http.MethodPost, "/api/v1/identity-providers/"+sso.ID+"/test", nil, http.StatusOK, &tested)
	if !tested.OK {
		t.Error("the provider's test failed")
	}
	admin.call(http.MethodPost, "/api/v1/setup/complete", nil, http.StatusNoContent, nil)
	if code, out := anon.do(http.MethodPost, "/api/v1/setup/admin", `{"username":"intruder","password":"<placeholder: password>"}`,
		map[string]string{"X-Setup-Token": setupToken}); code < 400 || code >= 500 {
		t.Errorf("the setup token still works after the wizard: %d %s", code, out)
	}

	// ---- A product, invited people and the product's owner.
	admin.call(http.MethodPost, "/api/v1/products", map[string]any{"code": "shop", "name": "Shop", "line": "Commerce"}, http.StatusCreated, nil)
	var invites struct {
		Items []struct{ Email, Link string }
	}
	admin.call(http.MethodPost, "/api/v1/invites", map[string]any{"emails": []string{"owner@example.com"}, "products": []string{"shop"}}, http.StatusCreated, &invites)
	if len(invites.Items) != 1 || invites.Items[0].Link == "" {
		t.Fatalf("invites without mail configured: %+v", invites.Items)
	}
	token := path.Base(invites.Items[0].Link)
	ownerB := srv.newBrowser(t)
	var preview struct{ Email string }
	ownerB.call(http.MethodGet, "/api/v1/auth/invites/"+token, nil, http.StatusOK, &preview)
	if preview.Email != "owner@example.com" {
		t.Errorf("the invitation is for %q", preview.Email)
	}
	var ownerMe me
	ownerB.call(http.MethodPost, "/api/v1/auth/invites/"+token+"/accept",
		map[string]any{"username": "owner", "display_name": "Product Owner", "password": "<placeholder: owner password>"}, http.StatusCreated, &ownerMe)
	if ownerMe.Status != "active" || !ownerMe.memberOf("shop", "member") {
		t.Errorf("the invited person: %+v", ownerMe)
	}
	if code, _ := srv.newBrowser(t).do(http.MethodPost, "/api/v1/auth/invites/"+token+"/accept",
		`{"username":"again","display_name":"Again","password":"<placeholder: password>"}`, nil); code != http.StatusGone {
		t.Errorf("an invitation link works twice: %d", code)
	}

	// The member signs in through the provider: a new account, waiting to be let in.
	memberB := srv.newBrowser(t)
	idp.SignIn(map[string]any{"sub": "u-member", "email": "member@example.com", "email_verified": true, "name": "Team Member", "preferred_username": "member"})
	authURL := memberB.redirect("/api/v1/auth/providers/" + sso.ID + "/start?return_to=/catalog")
	back := idp.Authorize(t, authURL)
	if landing := memberB.redirect("/api/v1/auth/providers/" + sso.ID + "/callback?" + back.Encode()); landing != srv.url+"/apply" {
		t.Errorf("someone new lands on %s", landing)
	}
	var memberMe me
	memberB.call(http.MethodGet, "/api/v1/me", nil, http.StatusOK, &memberMe)
	if memberMe.Status != "pending" || memberMe.Username != "member" {
		t.Fatalf("the member after signing in: %+v", memberMe)
	}

	// The administrator makes the invited person the product's owner and lets the member in.
	admin.call(http.MethodPut, "/api/v1/products/shop/members/"+ownerMe.ID, map[string]any{"role": "owner"}, http.StatusOK, nil)
	admin.call(http.MethodPut, "/api/v1/products/shop/members/"+memberMe.ID, map[string]any{"role": "member"}, http.StatusCreated, nil)
	memberB.call(http.MethodGet, "/api/v1/me", nil, http.StatusOK, &memberMe)
	if memberMe.Status != "active" || !memberMe.memberOf("shop", "member") {
		t.Errorf("the member after being added to the product: %+v", memberMe)
	}
	var shop struct {
		Owners      []struct{ Username string } `json:"owners"`
		MemberCount int                         `json:"member_count"`
	}
	admin.call(http.MethodGet, "/api/v1/products/shop", nil, http.StatusOK, &shop)
	if len(shop.Owners) != 1 || shop.Owners[0].Username != "owner" || shop.MemberCount != 2 {
		t.Errorf("the product: %+v", shop)
	}

	// ---- Connect the resource repository: import, check, publish.
	var source struct {
		ID       string `json:"id"`
		LastPull *struct {
			Result   string `json:"result"`
			Imported int    `json:"imported"`
			Failed   int    `json:"failed"`
		} `json:"last_pull"`
	}
	admin.call(http.MethodPost, "/api/v1/sources", map[string]any{"name": "resources", "url": repoURL, "branch": "main"}, http.StatusCreated, &source)
	for deadline := time.Now().Add(60 * time.Second); source.LastPull == nil || source.LastPull.Result == "running"; time.Sleep(200 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the repository was not pulled:\n%s", srv.log.String())
		}
		admin.call(http.MethodGet, "/api/v1/sources/"+source.ID, nil, http.StatusOK, &source)
	}
	if source.LastPull.Result != "ok" {
		t.Fatalf("the pull: %+v\n%s", *source.LastPull, srv.log.String())
	}

	type managed struct {
		Code     string `json:"code"`
		Origin   string `json:"origin"`
		Status   string `json:"status"`
		HasDraft bool   `json:"has_draft"`
		Checks   struct {
			Blocking int `json:"blocking"`
		} `json:"checks"`
	}
	var pending struct{ Items []managed }
	admin.call(http.MethodGet, "/api/v1/resources?pending=true", nil, http.StatusOK, &pending)
	imported := map[string]managed{}
	for _, r := range pending.Items {
		imported[r.Code] = r
	}
	for _, code := range []string{"code-review", "unit-testing", "release-notes", "shop-checkout", "jira", "gitlab", "probe", "leaky"} {
		r, ok := imported[code]
		if !ok || r.Origin != "repo" || !r.HasDraft {
			t.Errorf("%s was not imported as a draft: %+v", code, r)
		}
	}
	// The check stops the resource with a token in plain text, and only that one.
	for code, r := range imported {
		if blocked := r.Checks.Blocking > 0; blocked != (code == "leaky") {
			t.Errorf("%s: %d must-fix findings", code, r.Checks.Blocking)
		}
	}
	var leaky struct {
		Checks struct {
			Findings []struct {
				Rule     string `json:"rule"`
				Severity string `json:"severity"`
			} `json:"findings"`
		} `json:"checks"`
	}
	etag := admin.call(http.MethodGet, "/api/v1/resources/leaky/draft", nil, http.StatusOK, &leaky)
	if !slices.ContainsFunc(leaky.Checks.Findings, func(f struct {
		Rule     string `json:"rule"`
		Severity string `json:"severity"`
	}) bool {
		return f.Rule == "secrets" && f.Severity == "must_fix"
	}) {
		t.Errorf("leaky: no must-fix finding of the secrets rule: %+v", leaky.Checks.Findings)
	}
	if problem := admin.callProblem(http.MethodPost, "/api/v1/resources/leaky/publish", map[string]any{}, map[string]string{"If-Match": etag}); problem != "resource.check_blocked" {
		t.Errorf("publishing leaky: %s", problem)
	}
	// Registered on the web, a token in plain text is refused outright.
	if problem := admin.callProblem(http.MethodPut, "/api/v1/resources/web-leak/draft", map[string]any{
		"kind": "mcp", "meta": map[string]any{"name": "Web leak"},
		"definition": map[string]any{"server": map[string]any{"type": "http", "url": "https://mcp.example.com/mcp",
			"headers": map[string]any{"Authorization": "Bearer 8fK2mQ9xLp4ZrT7vW1nY3bHc6dJ0sGeA5uRk"}}, "delivery": "native"},
	}, nil); problem != "resource.plaintext_secret" {
		t.Errorf("registering a token in plain text: %s", problem)
	}
	for code := range imported {
		if code == "leaky" {
			continue
		}
		etag := admin.call(http.MethodGet, "/api/v1/resources/"+code+"/draft", nil, http.StatusOK, nil)
		admin.call(http.MethodPost, "/api/v1/resources/"+code+"/publish", map[string]any{"note": "First version"}, http.StatusCreated, nil, "If-Match", etag)
	}
	var published struct{ Items []managed }
	admin.call(http.MethodGet, "/api/v1/resources?status=published", nil, http.StatusOK, &published)
	if len(published.Items) != len(imported)-1 || slices.ContainsFunc(published.Items, func(r managed) bool { return r.Code == "leaky" || r.HasDraft }) {
		t.Errorf("published: %+v", published.Items)
	}

	// ---- Everyone connects with one command.
	owner.login(srv, ownerB)
	member.login(srv, memberB)
	var summary struct {
		People           int `json:"people_connected"`
		Devices          int `json:"devices_active"`
		RequiredComplete struct {
			People int `json:"people"`
			Of     int `json:"of"`
		} `json:"required_complete"`
		ByTool []struct {
			Tool   string `json:"tool"`
			People int    `json:"people"`
		} `json:"by_tool"`
	}
	admin.call(http.MethodGet, "/api/v1/installs/summary", nil, http.StatusOK, &summary)
	if summary.People != 2 || summary.Devices != 2 {
		t.Errorf("connected after login: %+v", summary)
	}

	// ---- The same skills and MCP in both tools; what the preview shows is what is written.
	planned := owner.ops(owner.must(exitOK, "--json", "sync", "--plan").stdout, "plan")
	if d := diffTrees(ownerBefore, snapshot(t, owner.home).without(dataDir)); d != "" {
		t.Fatalf("sync --plan changed the home directory:\n%s", d)
	}
	done := owner.ops(owner.must(exitOK, "--json", "sync", "--yes-global").stdout, "done")
	if !slices.Equal(planned, done) {
		t.Errorf("sync did something else than the preview showed:\nplanned:\n  %s\ndone:\n  %s", strings.Join(planned, "\n  "), strings.Join(done, "\n  "))
	}
	for _, want := range []string{
		"claude-code code-review tree", "codex code-review tree",
		"claude-code unit-testing tree", "codex unit-testing tree",
		"claude-code shop-checkout tree", "codex shop-checkout tree",
		"claude-code " + brand.MCPEntry + " mcpServers." + brand.MCPEntry, "codex " + brand.MCPEntry + " mcp_servers." + brand.MCPEntry,
	} {
		if !slices.ContainsFunc(done, func(op string) bool { return strings.HasPrefix(op, want) }) {
			t.Errorf("sync did not write %q:\n  %s", want, strings.Join(done, "\n  "))
		}
	}
	for _, w := range []*world{owner, member} {
		if w == member {
			// The member's own skill is in Codex's place for unit-testing: everything else is
			// written, that one is left alone, and the exit code says something was left out.
			w.must(exitPending, "sync", "--yes-global")
		}
		for _, tool := range []string{"claude", "codex"} {
			for _, code := range []string{"code-review", "unit-testing", "shop-checkout"} {
				if w == member && tool == "codex" && code == "unit-testing" {
					continue
				}
				if !strings.Contains(string(w.read(skillDir(tool, code)+"/SKILL.md")), "name: "+code) {
					t.Errorf("%s: %s is not the published skill", w.home, skillDir(tool, code))
				}
			}
		}
		w.checkSingleEntry()
	}
	if !bytes.Equal(member.read(".agents/skills/"+brand.Prefix+"unit-testing/SKILL.md"), memberBefore[".agents/skills/"+brand.Prefix+"unit-testing/SKILL.md"].data) {
		t.Error("the member's own skill was overwritten")
	}

	// The server knows who connected and where each resource is.
	type install struct {
		Tool, Status string
		Version      int
	}
	var item struct {
		InstalledPeople int       `json:"installed_people"`
		MyInstalls      []install `json:"my_installs"`
	}
	ownerB.call(http.MethodGet, "/api/v1/catalog/shop-checkout", nil, http.StatusOK, &item)
	if item.InstalledPeople != 2 || !slices.Contains(item.MyInstalls, install{"claude-code", "installed", 1}) || !slices.Contains(item.MyInstalls, install{"codex", "installed", 1}) {
		t.Errorf("shop-checkout as its owner sees it: %+v", item)
	}
	memberB.call(http.MethodGet, "/api/v1/catalog/unit-testing", nil, http.StatusOK, &item)
	if !slices.Contains(item.MyInstalls, install{"claude-code", "installed", 1}) || !slices.Contains(item.MyInstalls, install{"codex", "skipped", 1}) {
		t.Errorf("unit-testing as the member sees it: %+v", item)
	}
	admin.call(http.MethodGet, "/api/v1/installs/summary", nil, http.StatusOK, &summary)
	tools := map[string]int{}
	for _, bt := range summary.ByTool {
		tools[bt.Tool] = bt.People
	}
	// Both have every required resource: shop-checkout as a skill, Jira behind the single entry.
	if summary.People != 2 || summary.Devices != 2 || tools["claude-code"] != 2 || tools["codex"] != 2 ||
		summary.RequiredComplete.People != 2 || summary.RequiredComplete.Of != 2 {
		t.Errorf("the administrator's summary after syncing: %+v", summary)
	}
	memberB.call(http.MethodGet, "/api/v1/catalog/jira", nil, http.StatusOK, &item)
	if item.InstalledPeople != 2 || !slices.Contains(item.MyInstalls, install{"claude-code", "installed", 1}) || !slices.Contains(item.MyInstalls, install{"codex", "installed", 1}) {
		t.Errorf("jira, behind the single entry, as the member sees it: %+v", item)
	}

	// ---- The personal secret: set from the command line, never in a file, there for the MCP
	// server.
	member.mustInput(exitOK, mvpSecret+"\n", "secret", "set", "PROBE_TOKEN")
	var secrets struct {
		Items []struct {
			Name       string `json:"name"`
			Configured bool   `json:"configured"`
		}
	}
	status, out := memberB.do(http.MethodGet, "/api/v1/me/secrets", "", nil)
	if status != http.StatusOK || strings.Contains(string(out), mvpSecret) || json.Unmarshal(out, &secrets) != nil ||
		!slices.ContainsFunc(secrets.Items, func(s struct {
			Name       string `json:"name"`
			Configured bool   `json:"configured"`
		}) bool {
			return s.Name == "PROBE_TOKEN" && s.Configured
		}) {
		t.Errorf("the member's secrets: %d %s", status, out)
	}
	member.must(exitPending, "sync", "--yes-global")
	for _, w := range []*world{owner, member} {
		if found := holding(t, w.home, mvpSecret); len(found) != 0 {
			t.Errorf("files in a home directory hold the secret: %v", found)
		}
	}
	initialize := `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"e2e","version":"1"}}}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"
	// One server started directly (mcp.mode = direct).
	r := member.mustInput(exitOK, initialize+`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"whoami","arguments":{}}}`+"\n", "mcp", "run", "probe")
	if !strings.Contains(r.stdout, "token="+mvpSecret) {
		t.Errorf("rgd mcp run probe:\n%s\n%s", r.stdout, r.stderr)
	}
	if env, err := os.ReadFile(dump); err != nil || !slices.Contains(strings.Split(string(env), "\n"), "PROBE_TOKEN="+mvpSecret) {
		t.Errorf("the MCP server was not started with the secret in its environment: %v", err)
	}
	os.Remove(dump)
	// The single entry the tools start.
	r = member.mustInput(exitOK, initialize+`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"probe__whoami","arguments":{}}}`+"\n", "mcp", "serve")
	if !strings.Contains(r.stdout, "token="+mvpSecret) {
		t.Errorf("rgd mcp serve:\n%s\n%s", r.stdout, r.stderr)
	}
	// Someone without the secret is told which one to set.
	if r := owner.must(exitPending, "mcp", "call", "probe", "whoami", "{}"); !strings.Contains(r.stderr, "PROBE_TOKEN") {
		t.Errorf("rgd mcp call without the secret:\n%s", r.stderr)
	}
	db, err := os.ReadFile(filepath.Join(root, "server", brand.CLILong+".db"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(db, []byte(mvpSecret)) {
		t.Error("the server's database holds the secret in plain text")
	}

	// ---- Feedback: from the command line, handled by the product's owner, rated.
	r = member.must(exitOK, "--json", "feedback", "--type", "bug", "--title", "shop-checkout misses the coupon step",
		"-m", "The checkout check does not look at coupons.", "--resource", "shop-checkout")
	var sent struct{ Number int }
	if json.Unmarshal([]byte(r.stdout), &sent) != nil || sent.Number < 1 {
		t.Fatalf("rgd feedback: %s", r.stdout)
	}
	fb := fmt.Sprintf("/api/v1/feedback/%d", sent.Number)
	var listed struct {
		Items []struct{ Number int }
	}
	ownerB.call(http.MethodGet, "/api/v1/feedback?status=new", nil, http.StatusOK, &listed)
	if len(listed.Items) != 1 || listed.Items[0].Number != sent.Number {
		t.Errorf("the product's owner does not see the feedback: %+v", listed.Items)
	}
	type feedback struct {
		Status   string `json:"status"`
		Origin   string `json:"origin"`
		Score    int    `json:"score"`
		Assignee *struct {
			Username string `json:"username"`
		} `json:"assignee"`
		Events []struct {
			Kind string `json:"kind"`
		} `json:"events"`
	}
	var got feedback
	ownerB.call(http.MethodPatch, fb, map[string]any{"status": "accepted"}, http.StatusOK, &got)
	if got.Assignee == nil || got.Assignee.Username != "owner" {
		t.Errorf("accepted feedback: %+v", got)
	}
	ownerB.call(http.MethodPost, fb+"/comments", map[string]any{"body": "Fixed in the next version."}, http.StatusCreated, nil)
	ownerB.call(http.MethodPatch, fb, map[string]any{"status": "resolved"}, http.StatusOK, &got)
	memberB.call(http.MethodPost, fb+"/rate", map[string]any{"score": 5, "note": "Quick"}, http.StatusOK, &got)
	memberB.call(http.MethodGet, fb, nil, http.StatusOK, &got)
	kinds := []string{}
	for _, e := range got.Events {
		kinds = append(kinds, e.Kind)
	}
	if got.Status != "resolved" || got.Score != 5 || got.Origin != "cli" || !slices.Contains(kinds, "comment") {
		t.Errorf("the feedback in the end: %+v (events %v)", got, kinds)
	}

	// ---- The audit log has the whole way.
	status, out = admin.do(http.MethodGet, "/api/v1/audit/export", "", nil)
	if status != http.StatusOK {
		t.Fatalf("audit export: %d %s", status, out)
	}
	rows, err := csv.NewReader(bytes.NewReader(out)).ReadAll()
	if err != nil || len(rows) < 2 {
		t.Fatalf("audit export: %v\n%s", err, out)
	}
	col, method := slices.Index(rows[0], "action"), slices.Index(rows[0], "method")
	actions := map[string]int{}
	for _, row := range rows[1:] {
		actions[row[col]]++
		if row[col] == "auth.login" {
			actions["auth.login "+row[method]]++
		}
	}
	for _, want := range []string{
		"setup.admin_create", "identity_provider.create", "identity_provider.test", "setup.complete",
		"product.create", "invite.create", "invite.accept", "user.create", "product.member.add", "product.member.update",
		"source.manage", "resource.create", "resource.publish", "auth.login device_code", "auth.login oidc",
		"secret.set", "feedback.create", "feedback.manage",
	} {
		if actions[want] == 0 {
			t.Errorf("the audit log has no %s", want)
		}
	}
	if t.Failed() {
		names := make([]string, 0, len(actions))
		for a := range actions {
			names = append(names, a)
		}
		sort.Strings(names)
		t.Logf("actions in the audit log: %v", names)
	}
	if bytes.Contains(out, []byte(mvpSecret)) || bytes.Contains(out, []byte(idp.ClientSecret)) {
		t.Error("the audit log holds a secret")
	}

	if actions["auth.login device_code"] != 2 || actions["resource.publish"] != len(imported)-1 {
		t.Errorf("the audit log: %d sign-ins of a computer, %d publications", actions["auth.login device_code"], actions["resource.publish"])
	}

	// ---- Everyone uninstalls with one command, with the server gone; the chain is intact.
	srv.stop()
	verify := exec.Command(binary.BuildServer(t), "audit", "verify")
	verify.Env = srv.cmd.Env
	if out, err := verify.CombinedOutput(); err != nil || !strings.Contains(string(out), "intact") {
		t.Errorf("%s audit verify: %v\n%s", brand.Server, err, out)
	}
	for _, w := range []struct {
		w      *world
		before tree
	}{{owner, ownerBefore}, {member, memberBefore}} {
		w.w.must(exitOK, "uninstall")
		if d := diffTrees(w.before, snapshot(t, w.w.home).without(dataDir)); d != "" {
			t.Errorf("after uninstall %s is not what it was:\n%s", w.w.home, d)
		}
	}
}

// me is the signed-in person as the server answers (contract: Me).
type me struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	Status      string `json:"status"`
	Memberships []struct {
		Product struct {
			Code string `json:"code"`
		} `json:"product"`
		Role string `json:"role"`
	} `json:"memberships"`
}

func (m me) memberOf(product, role string) bool {
	for _, ms := range m.Memberships {
		if ms.Product.Code == product && ms.Role == role {
			return true
		}
	}
	return false
}

// call sends body as JSON, fails the test unless the answer has status want, decodes the answer
// into out when it is not nil, and returns the answer's ETag. header are pairs of a header's
// name and value.
func (b *browser) call(method, p string, body any, want int, out any, header ...string) string {
	b.t.Helper()
	raw := ""
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			b.t.Fatal(err)
		}
		raw = string(data)
	}
	h := map[string]string{}
	for i := 0; i+1 < len(header); i += 2 {
		h[header[i]] = header[i+1]
	}
	resp, data := b.send(method, p, raw, h)
	if resp.StatusCode != want {
		b.t.Fatalf("%s %s: %d, want %d\n%s", method, p, resp.StatusCode, want, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			b.t.Fatalf("%s %s: %v\n%s", method, p, err, data)
		}
	}
	return resp.Header.Get("ETag")
}

// callProblem sends body as JSON and returns the problem code of the answer, or its status when
// it is not a problem.
func (b *browser) callProblem(method, p string, body any, header map[string]string) string {
	b.t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		b.t.Fatal(err)
	}
	resp, out := b.send(method, p, string(data), header)
	var problem struct{ Code string }
	if resp.StatusCode < 400 || json.Unmarshal(out, &problem) != nil || problem.Code == "" {
		return fmt.Sprintf("%d %s", resp.StatusCode, out)
	}
	return problem.Code
}

// redirect follows one browser navigation and returns where it is sent next.
func (b *browser) redirect(p string) string {
	b.t.Helper()
	target := p
	if strings.HasPrefix(p, "/") {
		target = b.base + p
	}
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		b.t.Fatal(err)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		b.t.Fatalf("GET %s: %d, want a redirect", p, resp.StatusCode)
	}
	return resp.Header.Get("Location")
}

// ops returns the operations of an rgd --json sync document (field "done") or plan (field
// "plan"), one line each: the tool, the resource, the target (a directory tree, or the key in a
// configuration file) and the path relative to the home directory, sorted.
func (w *world) ops(out, field string) []string {
	w.t.Helper()
	var doc struct {
		Ops  []opLine `json:"ops"`
		Done []opLine `json:"done"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		w.t.Fatalf("not a JSON document: %v\n%s", err, out)
	}
	list := doc.Ops
	if field == "done" {
		list = doc.Done
	}
	var lines []string
	for _, op := range list {
		target := op.Kind
		if len(op.Key) > 0 {
			target = strings.Join(op.Key, ".")
		}
		rel, err := filepath.Rel(w.home, op.Path)
		if err != nil {
			rel = op.Path
		}
		lines = append(lines, fmt.Sprintf("%s %s %s %s", op.Tool, op.Resource, target, filepath.ToSlash(rel)))
	}
	sort.Strings(lines)
	return lines
}

type opLine struct {
	Tool     string   `json:"tool"`
	Resource string   `json:"resource"`
	Kind     string   `json:"kind"`
	Path     string   `json:"path"`
	Key      []string `json:"key"`
}

// checkSingleEntry checks that Claude Code and Codex each start this program's single MCP entry
// and nothing else of ours.
func (w *world) checkSingleEntry() {
	t := w.t
	t.Helper()
	std, err := hujson.Standardize(w.read(".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var claude struct {
		MCPServers map[string]struct {
			Command string
			Args    []string
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(std, &claude); err != nil {
		t.Fatal(err)
	}
	if e, ok := claude.MCPServers[brand.MCPEntry]; !ok || strings.Join(e.Args, " ") != "mcp serve" {
		t.Errorf("%s: Claude Code's single entry: %+v", w.home, claude.MCPServers)
	}
	for name := range claude.MCPServers {
		if strings.HasPrefix(name, brand.Prefix) {
			t.Errorf("%s: an entry of its own besides the single entry in Claude Code: %s", w.home, name)
		}
	}
	codex := string(w.read(".codex/config.toml"))
	if !strings.Contains(codex, "[mcp_servers."+brand.MCPEntry+"]") || strings.Contains(codex, "[mcp_servers."+brand.Prefix) {
		t.Errorf("%s: Codex's MCP servers:\n%s", w.home, codex)
	}
}

// mvpRepository makes the organisation's resource repository: the example repository, plus a
// skill for one product, an MCP server that is the test binary itself (probe, started as probe
// with dump as fakeMCPDump) and an MCP server with a token in plain text (leaky). It serves the
// repository over HTTPS on this machine and returns its address and the server's certificate.
func mvpRepository(t *testing.T, root, probe, dump string) (string, string) {
	t.Helper()
	work := filepath.Join(root, "repo")
	binary.CopyTree(t, filepath.Join(binary.Root(t), "examples", "resources"), work)
	files := map[string]string{
		brand.RepoConfig: `schema: 1
name: Example resources
locale: en
defaults:
  scope: org
  policy: recommended
  audience: rd
products:
  - { code: shop, name: Shop }
secrets:
  JIRA_API_TOKEN: { label: Jira API token }
  GITLAB_TOKEN: { label: GitLab access token }
  SHOP_TEST_API_TOKEN: { label: Shop test token }
  PROBE_TOKEN: { label: Probe token }
`,
		"skills/shop-checkout/SKILL.md":  "---\nname: shop-checkout\ndescription: Checks a change to the shop's checkout.\n---\n\nCheck the checkout steps one by one.\n",
		"skills/shop-checkout/meta.yaml": "name: Checkout check\nsummary: Checks a change to the checkout\nscope: shop\npolicy: required\nwhen: When the checkout changes\n",
		"mcp/probe.yaml": fmt.Sprintf(`meta:
  name: Probe
  summary: Says which token it got
  when: To check the personal secret
server:
  type: stdio
  command: '%s'
  env:
    PROBE_TOKEN: "${secret:PROBE_TOKEN}"
    %s: '%s'
delivery: native
tools:
  - { name: whoami, summary: Says which token it got }
`, probe, fakeMCPDump, dump),
		"mcp/leaky.yaml": `meta:
  name: Leaky
  summary: Has its token in the file
server:
  type: http
  url: https://leaky.example.com/mcp
  headers:
    Authorization: "Bearer 8fK2mQ9xLp4ZrT7vW1nY3bHc6dJ0sGeA5uRk"
`,
	}
	for name, data := range files {
		p := filepath.Join(work, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	binary.Repo(t, work)
	served := filepath.Join(root, "git")
	if err := os.MkdirAll(served, 0o755); err != nil {
		t.Fatal(err)
	}
	binary.Git(t, served, "clone", "-q", "--bare", work, "resources.git")
	base, ca := gitHTTPS(t, served)
	return base + "/resources.git", ca
}

// gitHTTPS serves the repositories under dir over HTTPS on a loopback port with git's own
// http-backend, the way a Git hosting service does, and returns the address and a file with the
// server's certificate (for GIT_SSL_CAINFO).
func gitHTTPS(t *testing.T, dir string) (string, string) {
	t.Helper()
	git := testenv.NeedGit(t)
	cmd := exec.Command(git, "--exec-path")
	cmd.Env = binary.GitEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(strings.TrimSpace(string(out)), binary.Exe("git-http-backend"))); err != nil {
		t.Skipf("git has no http-backend here: %v", err)
	}
	h := &cgi.Handler{Path: git, Args: []string{"http-backend"}, Dir: dir,
		Env: []string{"GIT_PROJECT_ROOT=" + dir, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1"}}
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	ca := filepath.Join(filepath.Dir(dir), "git-server.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(srv.URL)
	return "https://" + u.Host, ca
}

// fakeStdioMCP is the probe MCP server of TestMVPPath: it writes the environment it was started
// with to dump, then answers on standard input and output with one tool, whoami, which says
// which PROBE_TOKEN it got.
func fakeStdioMCP(dump string) int {
	if err := os.WriteFile(dump, []byte(strings.Join(os.Environ(), "\n")), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64<<10), 1<<20)
	out := json.NewEncoder(os.Stdout)
	for in.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(in.Bytes(), &m) != nil || len(m.ID) == 0 {
			continue
		}
		var result any = map[string]any{}
		switch m.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "probe", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{"name": "whoami", "description": "Says which token it got.", "inputSchema": map[string]any{"type": "object"}}}}
		case "tools/call":
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "token=" + os.Getenv("PROBE_TOKEN")}}}
		}
		if err := out.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result}); err != nil {
			return 1
		}
	}
	return 0
}

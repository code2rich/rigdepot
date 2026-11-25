// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package webui

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/code2rich/rigdepot/internal/testenv"
	"github.com/code2rich/rigdepot/web"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// builtIndex is index.html as the build leaves it: the markers, the boot
// script, the hashed assets.
const builtIndex = `<!doctype html>
<html lang="__LANG__" data-default-locale="__DEFAULT_LOCALE__">
<head><base href="__BASE_HREF__" />
<script nonce="__CSP_NONCE__">document.documentElement.dataset.theme = 'dark'</script>
<script type="module" crossorigin src="./assets/index-AbC123.js"></script>
</head><body><div id="app"></div></body></html>
`

func console() fstest.MapFS {
	return fstest.MapFS{
		"index.html":                 {Data: []byte(builtIndex)},
		"assets/index-AbC123.js":     {Data: []byte("console.log(1)")},
		"assets/index-AbC123.css":    {Data: []byte("body{}")},
		"fonts/martian-mono.woff2":   {Data: []byte("wOF2")},
		"brand/favicon.svg":          {Data: []byte("<svg/>")},
		"fonts/OFL-MartianMono.txt":  {Data: []byte("SIL Open Font License")},
		"assets/strange.unknownext":  {Data: []byte("?")},
		"assets/nested/chunk-1a2.js": {Data: []byte("export{}")},
	}
}

func newHandler(t *testing.T, opts Options) http.Handler {
	t.Helper()
	if opts.Files == nil && opts.DevProxy == nil {
		opts.Files = console()
	}
	h, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func get(t *testing.T, h http.Handler, method, target string, header ...string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var nonceAttr = regexp.MustCompile(`nonce="([^"]+)"`)

func TestPagesGetTheFilledInIndex(t *testing.T) {
	h := newHandler(t, Options{BasePath: "/hub", DefaultLocale: "zh-CN"})
	for _, path := range []string{"/", "/index.html", "/catalog", "/catalog/design-doc", "/admin/users", "/invite/abc-123"} {
		t.Run(path, func(t *testing.T) {
			resp := get(t, h, http.MethodGet, path)
			page := body(t, resp)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d", resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
				t.Errorf("Content-Type %q", ct)
			}
			if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
				t.Errorf("Cache-Control %q: the page carries a nonce of its own and must not be cached", cc)
			}
			for _, want := range []string{`<base href="/hub/" />`, `lang="zh-CN"`, `data-default-locale="zh-CN"`} {
				if !strings.Contains(page, want) {
					t.Errorf("page lacks %s:\n%s", want, page)
				}
			}
			if strings.Contains(page, "__") {
				t.Errorf("a marker was left in the page:\n%s", page)
			}
			m := nonceAttr.FindStringSubmatch(page)
			if m == nil {
				t.Fatalf("the boot script has no nonce:\n%s", page)
			}
			policy := resp.Header.Get("Content-Security-Policy")
			if !strings.Contains(policy, "script-src 'self' 'nonce-"+m[1]+"'") {
				t.Errorf("the policy %q does not allow the page's nonce %q", policy, m[1])
			}
		})
	}
}

func TestEveryPageGetsANewNonce(t *testing.T) {
	h := newHandler(t, Options{})
	seen := map[string]bool{}
	for range 20 {
		m := nonceAttr.FindStringSubmatch(body(t, get(t, h, http.MethodGet, "/")))
		if m == nil || len(m[1]) < 16 {
			t.Fatalf("nonce %v is missing or short", m)
		}
		if seen[m[1]] {
			t.Fatalf("nonce %q came twice", m[1])
		}
		seen[m[1]] = true
	}
}

func TestTheRootBasePath(t *testing.T) {
	page := body(t, get(t, newHandler(t, Options{}), http.MethodGet, "/"))
	if !strings.Contains(page, `<base href="/" />`) || !strings.Contains(page, `lang="en"`) {
		t.Errorf("at the root the base is / and the language defaults to en:\n%s", page)
	}
}

func TestSecurityHeadersOnEverything(t *testing.T) {
	for _, hsts := range []bool{false, true} {
		h := newHandler(t, Options{HSTS: hsts})
		for _, path := range []string{"/", "/catalog", "/assets/index-AbC123.js", "/brand/favicon.svg", "/missing.js"} {
			resp := get(t, h, http.MethodGet, path)
			policy := resp.Header.Get("Content-Security-Policy")
			for _, want := range []string{"default-src 'self'", "connect-src 'self'", "object-src 'none'", "frame-ancestors 'none'", "base-uri 'self'"} {
				if !strings.Contains(policy, want) {
					t.Errorf("%s: policy %q lacks %s", path, policy, want)
				}
			}
			if strings.Contains(policy, "unsafe-inline") || strings.Contains(policy, "unsafe-eval") || strings.Contains(policy, "*") {
				t.Errorf("%s: policy %q is not strict", path, policy)
			}
			for name, want := range map[string]string{
				"X-Content-Type-Options": "nosniff",
				"Referrer-Policy":        "same-origin",
				"X-Frame-Options":        "DENY",
			} {
				if got := resp.Header.Get(name); got != want {
					t.Errorf("%s: %s = %q, want %q", path, name, got, want)
				}
			}
			if got := resp.Header.Get("Strict-Transport-Security"); (got != "") != hsts {
				t.Errorf("%s with HSTS %v: Strict-Transport-Security = %q", path, hsts, got)
			}
		}
	}
}

func TestFilesAndTheirCaching(t *testing.T) {
	h := newHandler(t, Options{})
	tests := []struct {
		path, typ, cache, body string
	}{
		{"/assets/index-AbC123.js", "text/javascript; charset=utf-8", "public, max-age=31536000, immutable", "console.log(1)"},
		{"/assets/index-AbC123.css", "text/css; charset=utf-8", "public, max-age=31536000, immutable", "body{}"},
		{"/assets/nested/chunk-1a2.js", "text/javascript; charset=utf-8", "public, max-age=31536000, immutable", "export{}"},
		{"/fonts/martian-mono.woff2", "font/woff2", "no-cache", "wOF2"},
		{"/brand/favicon.svg", "image/svg+xml", "no-cache", "<svg/>"},
		{"/assets/strange.unknownext", "application/octet-stream", "public, max-age=31536000, immutable", "?"},
	}
	for _, tt := range tests {
		resp := get(t, h, http.MethodGet, tt.path)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d", tt.path, resp.StatusCode)
			continue
		}
		if got := resp.Header.Get("Content-Type"); got != tt.typ {
			t.Errorf("%s: Content-Type %q, want %q", tt.path, got, tt.typ)
		}
		if got := resp.Header.Get("Cache-Control"); got != tt.cache {
			t.Errorf("%s: Cache-Control %q, want %q", tt.path, got, tt.cache)
		}
		if got := body(t, resp); got != tt.body {
			t.Errorf("%s: body %q", tt.path, got)
		}
		etag := resp.Header.Get("ETag")
		if etag == "" {
			t.Errorf("%s: no ETag", tt.path)
			continue
		}
		again := get(t, h, http.MethodGet, tt.path, "If-None-Match", etag)
		if again.StatusCode != http.StatusNotModified {
			t.Errorf("%s with its ETag: status %d, want 304", tt.path, again.StatusCode)
		}
	}
}

func TestMissingFilesAreNotPages(t *testing.T) {
	h := newHandler(t, Options{})
	for _, path := range []string{"/assets/index-Old999.js", "/favicon.ico", "/brand/missing.svg", "/catalog/x.json"} {
		resp := get(t, h, http.MethodGet, path)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404 (a missing file must not get the page)", path, resp.StatusCode)
		}
		if strings.Contains(body(t, resp), "<html") {
			t.Errorf("%s: answered with the page", path)
		}
	}
}

func TestPathsCannotLeaveTheFiles(t *testing.T) {
	h := newHandler(t, Options{})
	for _, path := range []string{"/../index.html", "/assets/../../etc/passwd", "/%2e%2e/index.html"} {
		resp := get(t, h, http.MethodGet, path)
		if resp.StatusCode == http.StatusOK && !strings.Contains(body(t, resp), "<html") {
			t.Errorf("%s served something other than the page", path)
		}
	}
}

func TestOnlyGetAndHead(t *testing.T) {
	h := newHandler(t, Options{})
	head := get(t, h, http.MethodHead, "/catalog")
	if head.StatusCode != http.StatusOK || body(t, head) != "" || head.Header.Get("Content-Length") == "" {
		t.Errorf("HEAD: status %d, want 200 with a length and no body", head.StatusCode)
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		resp := get(t, h, m, "/")
		if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "GET, HEAD" {
			t.Errorf("%s: status %d, Allow %q", m, resp.StatusCode, resp.Header.Get("Allow"))
		}
	}
}

func TestBrokenBuildsAreRefused(t *testing.T) {
	without := func(marker string) fstest.MapFS {
		fs := console()
		fs["index.html"] = &fstest.MapFile{Data: []byte(strings.ReplaceAll(builtIndex, marker, "x"))}
		return fs
	}
	tests := map[string]fstest.MapFS{
		"no index.html": {"assets/a.js": {Data: []byte("1")}},
		"no base":       without(markerBase),
		"no nonce":      without(markerNonce),
		"no language":   without(markerLang),
	}
	for name, files := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := New(Options{Files: files}); err == nil {
				t.Error("New accepted it")
			}
		})
	}
	if _, err := New(Options{}); err == nil {
		t.Error("New accepted no files")
	}
}

// TestTheEmbeddedFilesWork serves what this build embeds: the stub page
// without -tags webdist, the built console with it. Either has to pass New
// and fill in its markers.
func TestTheEmbeddedFilesWork(t *testing.T) {
	h := newHandler(t, Options{Files: web.FS(), BasePath: "/hub"})
	resp := get(t, h, http.MethodGet, "/some/page")
	page := body(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, `<base href="/hub/" />`) || strings.Contains(page, "__") {
		t.Errorf("status %d, page:\n%s", resp.StatusCode, page)
	}
	if !web.Built && !strings.Contains(page, "go run ./tools/dev web") {
		t.Errorf("the stub page does not say how to build the console:\n%s", page)
	}
}

func TestTheDevelopmentProxy(t *testing.T) {
	var asked []string
	dev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, ".ts") {
			w.Header().Set("Content-Type", "text/javascript")
			io.WriteString(w, "export const x = 1")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Etag", `W/"dev"`)
		if strings.Contains(r.Header.Get("Accept-Encoding"), "br") {
			t.Errorf("the browser's encodings reached the development server: %q", r.Header.Get("Accept-Encoding"))
		}
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			// A compressed page has to be filled in all the same.
			w.Header().Set("Content-Encoding", "gzip")
			zw := gzip.NewWriter(w)
			io.WriteString(zw, builtIndex)
			zw.Close()
			return
		}
		io.WriteString(w, builtIndex)
	}))
	defer dev.Close()
	target, _ := url.Parse(dev.URL)
	h := newHandler(t, Options{DevProxy: target, DefaultLocale: "zh-CN"})

	resp := get(t, h, http.MethodGet, "/catalog", "Accept-Encoding", "br, gzip")
	page := body(t, resp)
	m := nonceAttr.FindStringSubmatch(page)
	if resp.StatusCode != http.StatusOK || m == nil || strings.Contains(page, "__") || !strings.Contains(page, `lang="zh-CN"`) {
		t.Fatalf("status %d, page:\n%s", resp.StatusCode, page)
	}
	policy := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(policy, "'nonce-"+m[1]+"'") || !strings.Contains(policy, "style-src 'self' 'unsafe-inline'") {
		t.Errorf("development policy %q: want the page's nonce for scripts and inline styles for the dev server", policy)
	}
	if n := len(resp.Header.Values("Content-Security-Policy")); n != 1 {
		t.Errorf("%d policies; two would both apply and block each other's nonce", n)
	}
	if resp.Header.Get("Etag") != "" {
		t.Error("the filled-in page kept the development server's ETag")
	}

	script := get(t, h, http.MethodGet, "/src/main.ts")
	if got := body(t, script); got != "export const x = 1" {
		t.Errorf("a module came back as %q", got)
	}
	if strings.Join(asked, " ") != "/catalog /src/main.ts" {
		t.Errorf("the development server was asked for %v", asked)
	}

	dev.Close()
	down := get(t, h, http.MethodGet, "/")
	if down.StatusCode != http.StatusBadGateway || !strings.Contains(body(t, down), "pnpm --dir web dev") {
		t.Errorf("with the development server down: status %d", down.StatusCode)
	}
}

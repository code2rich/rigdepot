// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package webui

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// Options configure the console handler.
type Options struct {
	// Files are the console's files with index.html at the root: the built
	// console or the stub page (web.FS()).
	Files fs.FS
	// BasePath is the path the server is reached under, without the trailing
	// slash: "" at the root, "/hub" behind a proxy at /hub/. The handler sees
	// requests with it already removed; it only writes it into <base href>.
	BasePath string
	// DefaultLocale is the language of the page until the console has picked
	// one (RGD_DEFAULT_LOCALE).
	DefaultLocale string
	// HSTS adds Strict-Transport-Security; set when the server is reached
	// over HTTPS.
	HSTS bool
	// DevProxy, when set, is the console's development server on this
	// machine: every request goes there instead of to Files, and index.html is
	// filled in on the way back. Development only.
	DevProxy *url.URL
	// Log receives the problems of the development proxy.
	Log *slog.Logger
}

// The markers in index.html that the handler replaces on every request.
const (
	markerBase    = "__BASE_HREF__"
	markerLang    = "__LANG__"
	markerDefault = "__DEFAULT_LOCALE__"
	markerNonce   = "__CSP_NONCE__"
)

// requiredMarkers must all be in index.html: without them the page would
// carry the wrong base path or be blocked by its own content security policy.
var requiredMarkers = []string{markerBase, markerLang, markerNonce}

// immutablePrefix is where the build puts files whose names carry a hash
// of their content; they can be cached for good.
const immutablePrefix = "assets/"

// contentTypes are the types of the files a build contains. Anything else is
// served as a download, never sniffed.
var contentTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".json":  "application/json",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".ico":   "image/x-icon",
	".woff2": "font/woff2",
	".txt":   "text/plain; charset=utf-8",
	".map":   "application/json",
}

type file struct {
	data []byte
	etag string
	typ  string
}

type handler struct {
	opts  Options
	index []byte
	files map[string]file // by path relative to the root, with slashes
	proxy *httputil.ReverseProxy
}

// New returns the handler that serves the console: index.html for the
// console's own paths, the built files with their cache headers, and the
// security headers on everything. It fails when index.html is missing or
// lacks a marker the server fills in.
func New(opts Options) (http.Handler, error) {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.DefaultLocale == "" {
		opts.DefaultLocale = "en"
	}
	h := &handler{opts: opts}
	if opts.DevProxy != nil {
		h.proxy = h.devProxy(opts.DevProxy)
		return h, nil
	}
	if opts.Files == nil {
		return nil, errors.New("webui: no files")
	}
	h.files = map[string]file{}
	err := fs.WalkDir(opts.Files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(opts.Files, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		typ, ok := contentTypes[strings.ToLower(path.Ext(p))]
		if !ok {
			typ = "application/octet-stream"
		}
		h.files[p] = file{data: data, etag: `"` + hex.EncodeToString(sum[:16]) + `"`, typ: typ}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("webui: read the console's files: %w", err)
	}
	index, ok := h.files["index.html"]
	if !ok {
		return nil, errors.New("webui: the console's files have no index.html")
	}
	if err := checkMarkers(index.data); err != nil {
		return nil, err
	}
	h.index = index.data
	return h, nil
}

func checkMarkers(index []byte) error {
	var missing []string
	for _, m := range requiredMarkers {
		if !bytes.Contains(index, []byte(m)) {
			missing = append(missing, m)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("webui: index.html lacks %s, which the server fills in (web/index.html)", strings.Join(missing, ", "))
	}
	return nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.proxy != nil {
		h.proxy.ServeHTTP(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.securityHeaders(w.Header(), "")
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if p == "" || p == "index.html" {
		h.serveIndex(w, r)
		return
	}
	if f, ok := h.files[p]; ok {
		h.securityHeaders(w.Header(), "")
		cache := "no-cache"
		if strings.HasPrefix(p, immutablePrefix) {
			cache = "public, max-age=31536000, immutable"
		}
		w.Header().Set("Cache-Control", cache)
		w.Header().Set("Content-Type", f.typ)
		w.Header().Set("ETag", f.etag)
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(f.data))
		return
	}
	// A path whose last part looks like a file name is a file that does not
	// exist: answering with the page would hand a script the HTML.
	if strings.Contains(path.Base(p), ".") {
		h.securityHeaders(w.Header(), "")
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	// Every other path is one of the console's own pages; its router decides.
	h.serveIndex(w, r)
}

// serveIndex fills in index.html for this request.
func (h *handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	nonce := newNonce()
	body := h.render(h.index, nonce)
	h.securityHeaders(w.Header(), nonce)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write(body)
	}
}

// render replaces the markers of index.html. Every value is escaped for an
// HTML attribute, although none comes from the request.
func (h *handler) render(index []byte, nonce string) []byte {
	locale := html.EscapeString(h.opts.DefaultLocale)
	return []byte(strings.NewReplacer(
		markerBase, html.EscapeString(h.opts.BasePath+"/"),
		markerLang, locale,
		markerDefault, locale,
		markerNonce, nonce,
	).Replace(string(index)))
}

// csp is the content security policy of the console: everything from the
// server itself, scripts and styles also with the nonce of this response
// (when it is a page), nothing framed, no plugins. dev is the variant for the
// development server, whose style injection needs inline styles; a nonce in
// style-src would switch 'unsafe-inline' off, so that one has none.
func csp(nonce string, dev bool) string {
	withNonce := ""
	if nonce != "" {
		withNonce = " 'nonce-" + nonce + "'"
	}
	style := "style-src 'self'" + withNonce
	if dev {
		style = "style-src 'self' 'unsafe-inline'"
	}
	return "default-src 'self'; " +
		"script-src 'self'" + withNonce + "; " +
		style + "; " +
		"img-src 'self'; font-src 'self'; connect-src 'self'; " +
		"object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"
}

// securityHeaders sets the headers of the security baseline
// (docs/architecture/06-server.md#安全基线). nonce is "" for responses that
// are not pages.
func (h *handler) securityHeaders(hd http.Header, nonce string) {
	hd.Set("Content-Security-Policy", csp(nonce, h.proxy != nil))
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Referrer-Policy", "same-origin")
	hd.Set("X-Frame-Options", "DENY")
	if h.opts.HSTS {
		hd.Set("Strict-Transport-Security", "max-age=31536000")
	}
}

func newNonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(errors.New("no randomness: " + err.Error()))
	}
	return base64.RawStdEncoding.EncodeToString(b[:])
}

// devProxy forwards everything to the development server and fills in the
// pages it returns, so that the console behaves as it does when embedded:
// same markers, same headers.
func (h *handler) devProxy(target *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = target.Host
			// Compressed pages could not be filled in.
			r.Out.Header.Del("Accept-Encoding")
		},
		ModifyResponse: func(resp *http.Response) error {
			if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
				h.securityHeaders(resp.Header, "")
				return nil
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				return err
			}
			nonce := newNonce()
			body = h.render(body, nonce)
			resp.Body = io.NopCloser(bytes.NewReader(body))
			resp.ContentLength = int64(len(body))
			resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
			resp.Header.Set("Cache-Control", "no-store")
			resp.Header.Del("Etag") // the page differs from the one the tag names
			h.securityHeaders(resp.Header, nonce)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			h.opts.Log.WarnContext(r.Context(), "the console's development server does not answer", "target", target.String(), "err", err)
			h.securityHeaders(w.Header(), "")
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, "The console's development server at %s does not answer. Start it with: pnpm --dir web dev\n", target)
		},
	}
}

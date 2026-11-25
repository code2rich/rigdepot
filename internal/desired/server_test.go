// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package desired

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/code2rich/rigdepot/internal/apiclient"
	"github.com/code2rich/rigdepot/internal/bundle"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/testenv"
)

// fakeServer stands in for the server's sync operations: the desired state it is given, the
// bundles, and how it answers.
type fakeServer struct {
	srv     *httptest.Server
	mu      sync.Mutex
	doc     apiclient.DesiredState
	etag    string
	bundles map[string][]byte
	status  int // answer every request with this status when not 0
	// asked counts the requests by path; sawETag is the If-None-Match of the last desired state
	// request.
	asked   map[string]int
	sawETag string
}

func newFakeServer(t *testing.T) *fakeServer {
	f := &fakeServer{bundles: map[string][]byte{}, asked: map[string]int{}, etag: `"d-1"`}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.asked[r.URL.Path]++
		if f.status != 0 {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(f.status)
			json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": "x", "status": f.status, "code": "auth.required"})
			return
		}
		switch {
		case r.URL.Path == "/api/v1/sync/desired":
			f.sawETag = r.Header.Get("If-None-Match")
			w.Header().Set("ETag", f.etag)
			if f.sawETag == f.etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(f.doc)
		case strings.HasPrefix(r.URL.Path, "/api/v1/sync/bundles/"):
			data, ok := f.bundles[strings.TrimPrefix(r.URL.Path, "/api/v1/sync/bundles/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/x-tar")
			w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// publish puts a skill into the server's desired state.
func (f *fakeServer) publish(t *testing.T, code string, version int, body string) {
	t.Helper()
	files := fstest.MapFS{"SKILL.md": {Data: []byte("---\nname: " + code + "\ndescription: x\n---\n" + body)}}
	packed, err := bundle.Pack(files)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := bundle.Digest(files)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bundles[packed.Address] = packed.Data
	policy := apiclient.Recommended
	f.doc.Resources = append(f.doc.Resources, apiclient.DesiredResource{Code: code, Kind: "skill", Version: version,
		Digest: string(digest), Bundle: packed.Address, Size: len(packed.Data), Reason: "policy",
		Meta: apiclient.ResourceMeta{Name: code, Policy: &policy}})
}

func (f *fakeServer) client(t *testing.T) *apiclient.ClientWithResponses {
	t.Helper()
	c, err := apiclient.Connect(apiclient.Options{Server: f.srv.URL, Token: "rgdd_<placeholder: device token>"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func dataDir(t *testing.T) home.Dir {
	t.Helper()
	d, err := home.At(filepath.Join(testenv.Current(t).Home, t.Name(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestFromServer(t *testing.T) {
	f := newFakeServer(t)
	f.doc.Cli = apiclient.CLIVersions{MinVersion: "0.4.0", LatestVersion: "0.5.0"}
	f.doc.Defaults = apiclient.OrgDefaults{ToolsDisabled: []string{"opencode"}, McpMode: "single-entry"}
	f.doc.User = apiclient.UserRef{Username: "jdoe"}
	f.publish(t, "unit-testing", 3, "Steps.\n")
	f.publish(t, "review", 1, "More steps.\n")
	o := ServerOptions{URL: f.srv.URL, Data: dataDir(t), Kinds: kinds(), Version: "0.5.0"}
	ctx := context.Background()

	st, err := FromServer(ctx, f.client(t), o)
	if err != nil {
		t.Fatal(err)
	}
	if st.Cached || len(st.Resources) != 2 || len(st.Failed) != 0 || st.Source.Type != SourceServer || st.Source.URL != f.srv.URL {
		t.Fatalf("state: %+v", st)
	}
	r, ok := st.Lookup("unit-testing")
	if !ok || r.Ref.Version != 3 || r.Spec != "unit-testing" || r.Meta.Policy != "recommended" || r.Reason != "policy" || !r.Digest.Valid() {
		t.Errorf("unit-testing: %+v", r)
	}
	if st.Org.User != "jdoe" || st.Org.ToolsDisabled[0] != "opencode" || st.Org.MinVersion != "0.4.0" {
		t.Errorf("org: %+v", st.Org)
	}
	if _, err := os.Stat(o.Data.Join(DesiredFile)); err != nil {
		t.Errorf("the state is not kept: %v", err)
	}

	// Again: the server says nothing changed; the bundles come from the cache.
	if st, err = FromServer(ctx, f.client(t), o); err != nil || len(st.Resources) != 2 || st.Cached {
		t.Fatalf("not modified: %+v %v", st, err)
	}
	if f.sawETag != `"d-1"` || f.asked["/api/v1/sync/desired"] != 2 || len(f.asked) != 3 {
		t.Errorf("requests: %v, If-None-Match %q", f.asked, f.sawETag)
	}

	// The server goes away: the kept state is used, marked as such.
	f.srv.Close()
	st, err = FromServer(ctx, f.client(t), o)
	if err != nil || !st.Cached || st.Unreachable == nil || len(st.Resources) != 2 {
		t.Fatalf("unreachable: %+v %v", st, err)
	}
	// Without a kept state there is nothing to fall back on.
	other := o
	other.Data = dataDir(t)
	other.Data, _ = home.At(filepath.Join(other.Data.Path(), "other"))
	if _, err := FromServer(ctx, f.client(t), other); !errors.Is(err, ErrUnreachable) {
		t.Errorf("unreachable without a kept state: %v", err)
	}
	// A kept state of another server is not used.
	other.URL = "https://hub.example.com"
	other.Data = o.Data
	if _, err := FromServer(ctx, f.client(t), other); !errors.Is(err, ErrUnreachable) {
		t.Errorf("another server's kept state: %v", err)
	}
}

func TestFromServerRefusals(t *testing.T) {
	ctx := context.Background()
	f := newFakeServer(t)
	f.doc.Cli = apiclient.CLIVersions{MinVersion: "0.6.0", LatestVersion: "0.6.0"}
	o := ServerOptions{URL: f.srv.URL, Data: dataDir(t), Kinds: kinds(), Version: "0.5.0"}
	var old *TooOldError
	if _, err := FromServer(ctx, f.client(t), o); !errors.As(err, &old) || old.Min != "0.6.0" || !errors.Is(err, ErrTooOld) {
		t.Errorf("too old: %v", err)
	}
	// A development build is never too old.
	o.Version = "0.0.0-dev+1a2b3c4d5e6f"
	if _, err := FromServer(ctx, f.client(t), o); err != nil {
		t.Errorf("development build: %v", err)
	}
	f.status = http.StatusUnauthorized
	if _, err := FromServer(ctx, f.client(t), o); !errors.Is(err, ErrSignedOut) {
		t.Errorf("signed out: %v", err)
	}
	// A server error is not "unreachable": the kept state is not used for it.
	f.status = http.StatusInternalServerError
	var ae *apiclient.Error
	if _, err := FromServer(ctx, f.client(t), o); !errors.As(err, &ae) || errors.Is(err, ErrUnreachable) {
		t.Errorf("server error: %v", err)
	}
}

// A resource whose bundle cannot be had or does not match its address fails alone: what was
// written for it is held, the others are delivered.
func TestFromServerBundleFailures(t *testing.T) {
	f := newFakeServer(t)
	f.publish(t, "unit-testing", 1, "Steps.\n")
	f.publish(t, "gone", 1, "x\n")
	f.publish(t, "forged", 1, "y\n")
	f.publish(t, "strange", 1, "z\n")
	for i, r := range f.doc.Resources {
		switch r.Code {
		case "gone":
			delete(f.bundles, r.Bundle)
		case "forged":
			f.bundles[r.Bundle] = append([]byte{}, f.bundles[f.doc.Resources[0].Bundle]...)
		case "strange":
			f.doc.Resources[i].Kind = "hologram"
		}
	}
	st, err := FromServer(context.Background(), f.client(t), ServerOptions{URL: f.srv.URL, Data: dataDir(t), Kinds: kinds()})
	if err != nil {
		t.Fatal(err)
	}
	var failed []string
	for _, x := range st.Failed {
		failed = append(failed, x.Ref.Code+":"+x.Issues[0].Code)
	}
	if len(st.Resources) != 1 || strings.Join(failed, ",") != "forged:bundle_unavailable,gone:bundle_unavailable,strange:unknown_kind" {
		t.Errorf("delivered %d, failed %v", len(st.Resources), failed)
	}
}

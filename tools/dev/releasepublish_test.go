// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestVersionTag(t *testing.T) {
	for tag, want := range map[string]bool{
		"v0.1.0": true, "v1.20.3": true, "v0.4.0-rc.1": true, "v0.4.0-rc.12": true,
		"0.1.0": false, "v0.1": false, "v01.0.0": false, "v0.1.0-rc.0": false, "v0.1.0-beta.1": false, "v0.1.0+meta": false,
	} {
		if got := versionTag.MatchString(tag); got != want {
			t.Errorf("%s: %v, want %v", tag, got, want)
		}
	}
}

func TestChangelogSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CHANGELOG.md")
	if err := os.WriteFile(path, []byte("# Changelog\n\nIntro.\n\n## 0.2.0 (unreleased)\n\nTwo.\n\n## 0.1.0 — 2026-11-25\n\nOne.\n\n### Known limits\n\n- Some.\n\n## 0.0.9\n\nOld.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for version, want := range map[string]string{
		"0.1.0":      "One.\n\n### Known limits\n\n- Some.\n",
		"0.2.0":      "Two.\n",
		"0.2.0-rc.1": "Two.\n", // a release candidate takes the notes of its version
	} {
		got, err := changelogSection(path, version)
		if err != nil || got != want {
			t.Errorf("%s: %q, %v; want %q", version, got, err, want)
		}
	}
	if _, err := changelogSection(path, "0.3.0"); err == nil || !strings.Contains(err.Error(), `"## 0.3.0"`) {
		t.Errorf("a version without notes: %v", err)
	}
}

// fakeGitHub is the part of the GitHub API the release task uses, on loopback.
type fakeGitHub struct {
	mu       sync.Mutex
	releases map[int64]*githubRelease
	files    map[int64][]byte // asset id -> content
	next     int64
	auth     []string
}

func newFakeGitHub(t *testing.T) (*fakeGitHub, *githubClient) {
	f := &fakeGitHub{releases: map[int64]*githubRelease{}, files: map[int64][]byte{}, next: 100}
	f.releases[7] = &githubRelease{ID: 7, TagName: "v0.1.0", Draft: true}
	f.releases[3] = &githubRelease{ID: 3, TagName: "v0.0.9"}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, &githubClient{repo: "example/project", token: "<placeholder: token>", api: srv.URL, upload: srv.URL, http: srv.Client()}
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	p := strings.TrimPrefix(r.URL.Path, "/repos/example/project/releases")
	switch {
	case r.Method == "GET" && p == "":
		var list []githubRelease
		for _, id := range []int64{3, 7} {
			list = append(list, *f.releases[id])
		}
		json.NewEncoder(w).Encode(list)
	case r.Method == "GET" && p == "/7":
		json.NewEncoder(w).Encode(githubRelease{ID: 7, TagName: "v0.1.0", Draft: f.releases[7].Draft})
	case r.Method == "GET" && p == "/7/assets":
		json.NewEncoder(w).Encode(f.releases[7].Assets)
	case r.Method == "POST" && p == "/7/assets":
		data, _ := io.ReadAll(r.Body)
		f.next++
		f.files[f.next] = data
		f.releases[7].Assets = append(f.releases[7].Assets, githubAsset{ID: f.next, Name: r.URL.Query().Get("name"), Size: int64(len(data))})
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("{}"))
	case r.Method == "DELETE" && strings.HasPrefix(p, "/assets/"):
		var kept []githubAsset
		for _, a := range f.releases[7].Assets {
			if "/assets/"+itoa(a.ID) != p {
				kept = append(kept, a)
			}
		}
		f.releases[7].Assets = kept
		w.WriteHeader(http.StatusNoContent)
	case r.Method == "GET" && strings.HasPrefix(p, "/assets/") && r.Header.Get("Accept") == "application/octet-stream":
		for id, data := range f.files {
			if "/assets/"+itoa(id) == p {
				w.Write(data)
				return
			}
		}
		http.NotFound(w, r)
	case r.Method == "PATCH" && p == "/7":
		var body struct {
			Draft      bool   `json:"draft"`
			MakeLatest string `json:"make_latest"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.MakeLatest != "true" {
			http.Error(w, "make_latest", http.StatusBadRequest)
			return
		}
		f.releases[7].Draft = body.Draft
		w.Write([]byte("{}"))
	default:
		http.Error(w, r.Method+" "+r.URL.Path, http.StatusNotFound)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestGitHubClientDraftRoundTrip(t *testing.T) {
	f, gh := newFakeGitHub(t)
	if _, err := gh.draftByTag("v0.0.9"); err == nil {
		t.Error("a published release was taken for a draft")
	}
	rel, err := gh.draftByTag("v0.1.0")
	if err != nil || rel.ID != 7 {
		t.Fatalf("draftByTag: %+v, %v", rel, err)
	}

	dir := t.TempDir()
	sums := filepath.Join(dir, "checksums.txt")
	os.WriteFile(sums, []byte("old\n"), 0o644)
	if err := gh.uploadAsset(7, sums); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(sums, []byte("new and longer\n"), 0o644)
	rel, _ = gh.release(7)
	if err := gh.deleteAsset(rel.Assets[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := gh.uploadAsset(7, sums); err != nil {
		t.Fatal(err)
	}
	rel, err = gh.release(7)
	if err != nil || len(rel.Assets) != 1 || rel.Assets[0].Name != "checksums.txt" || rel.Assets[0].Size != int64(len("new and longer\n")) {
		t.Fatalf("assets after replacing: %+v, %v", rel.Assets, err)
	}
	data, err := gh.downloadAsset(rel.Assets[0].ID)
	if err != nil || string(data) != "new and longer\n" {
		t.Errorf("download: %q, %v", data, err)
	}
	if err := gh.publishDraft(7, "true"); err != nil || f.releases[7].Draft {
		t.Errorf("publish: %v, draft %v", err, f.releases[7].Draft)
	}
	for _, a := range f.auth {
		if a != "Bearer <placeholder: token>" {
			t.Errorf("a request went without the token: %q", a)
		}
	}
}

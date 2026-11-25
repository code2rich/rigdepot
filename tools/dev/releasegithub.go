// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// githubClient is the little of the GitHub REST API that release needs after goreleaser has
// created the draft: find it, replace and add files, read them back, publish it. Only the
// standard library, like the rest of tools/dev.
type githubClient struct {
	repo   string // owner/name
	token  string
	api    string // https://api.github.com
	upload string // https://uploads.github.com
	http   *http.Client
}

func newGitHubClient(repo, token string) *githubClient {
	api := os.Getenv("GITHUB_API_URL") // set in GitHub Actions; GitHub Enterprise has its own
	if api == "" {
		api = "https://api.github.com"
	}
	return &githubClient{repo: repo, token: token, api: api, upload: "https://uploads.github.com", http: &http.Client{Timeout: 30 * time.Minute}}
}

type githubRelease struct {
	ID      int64         `json:"id"`
	TagName string        `json:"tag_name"`
	Draft   bool          `json:"draft"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// do sends one request and decodes a JSON answer into out (when not nil). want is the status
// that counts as success.
func (c *githubClient) do(method, url string, body io.Reader, size int64, contentType string, want int, out any) error {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if size >= 0 && body != nil {
		req.ContentLength = size
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != want {
		return fmt.Errorf("%s %s: %s\n%s", method, url, resp.Status, bytes.TrimSpace(data))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("%s %s: %w", method, url, err)
		}
	}
	return nil
}

// draftByTag finds the draft release of a tag. A draft is not found by its tag
// (GET /releases/tags/{tag} only knows published releases), so the releases are listed.
func (c *githubClient) draftByTag(tag string) (githubRelease, error) {
	for page := 1; page <= 10; page++ {
		var list []githubRelease
		if err := c.do("GET", fmt.Sprintf("%s/repos/%s/releases?per_page=100&page=%d", c.api, c.repo, page), nil, -1, "", http.StatusOK, &list); err != nil {
			return githubRelease{}, err
		}
		for _, r := range list {
			if r.Draft && r.TagName == tag {
				return c.release(r.ID)
			}
		}
		if len(list) < 100 {
			break
		}
	}
	return githubRelease{}, fmt.Errorf("no draft release of %s in %s", tag, c.repo)
}

// release reads one release with all its files.
func (c *githubClient) release(id int64) (githubRelease, error) {
	var r githubRelease
	if err := c.do("GET", fmt.Sprintf("%s/repos/%s/releases/%d", c.api, c.repo, id), nil, -1, "", http.StatusOK, &r); err != nil {
		return r, err
	}
	r.Assets = nil
	for page := 1; ; page++ {
		var assets []githubAsset
		if err := c.do("GET", fmt.Sprintf("%s/repos/%s/releases/%d/assets?per_page=100&page=%d", c.api, c.repo, id, page), nil, -1, "", http.StatusOK, &assets); err != nil {
			return r, err
		}
		r.Assets = append(r.Assets, assets...)
		if len(assets) < 100 {
			return r, nil
		}
	}
}

func (c *githubClient) deleteAsset(id int64) error {
	return c.do("DELETE", fmt.Sprintf("%s/repos/%s/releases/assets/%d", c.api, c.repo, id), nil, -1, "", http.StatusNoContent, nil)
}

// uploadAsset adds a file to a release under its base name.
func (c *githubClient) uploadAsset(releaseID int64, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/repos/%s/releases/%d/assets?name=%s", c.upload, c.repo, releaseID, url.QueryEscape(filepath.Base(path)))
	return c.do("POST", u, f, info.Size(), "application/octet-stream", http.StatusCreated, nil)
}

// downloadAsset reads a file of a release, drafts included. GitHub answers with a redirect to
// the storage, which net/http follows without the Authorization header.
func (c *githubClient) downloadAsset(id int64) ([]byte, error) {
	req, err := http.NewRequest("GET", fmt.Sprintf("%s/repos/%s/releases/assets/%d", c.api, c.repo, id), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading asset %d: %s", id, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// publishDraft makes a draft the release. latest is "true" or "false" (a pre-release).
func (c *githubClient) publishDraft(id int64, latest string) error {
	body, _ := json.Marshal(map[string]any{"draft": false, "make_latest": latest})
	return c.do("PATCH", fmt.Sprintf("%s/repos/%s/releases/%s", c.api, c.repo, strconv.FormatInt(id, 10)), bytes.NewReader(body), int64(len(body)), "application/json", http.StatusOK, nil)
}

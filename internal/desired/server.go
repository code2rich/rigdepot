// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package desired

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/apiclient"
	"github.com/code2rich/rigdepot/internal/bundle"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/repoformat"
)

// Server is the part of the server's API a desired state comes from: *apiclient.ClientWithResponses
// with the device token.
type Server interface {
	GetDesiredStateWithResponse(ctx context.Context, params *apiclient.GetDesiredStateParams, reqEditors ...apiclient.RequestEditorFn) (*apiclient.GetDesiredStateResponse, error)
	GetBundleWithResponse(ctx context.Context, digest apiclient.Digest, reqEditors ...apiclient.RequestEditorFn) (*apiclient.GetBundleResponse, error)
}

// ServerOptions say where a desired state from the server is kept and checked.
type ServerOptions struct {
	// URL is the server's address, as the manifest records the source.
	URL string
	// Data is the local data directory: the last desired state is kept in DesiredFile, the
	// bundles in BundlesDir.
	Data home.Dir
	// Kinds lists the resource kinds; nil means the program's registry.
	Kinds repoformat.Kinds
	// Version is this program's version, checked against the minimum the server accepts. A
	// development build is never too old.
	Version string
}

// DesiredFile is where the last desired state from the server is kept, relative to the local
// data directory. A sync uses it when the server cannot be reached.
const DesiredFile = home.StateDir + "/desired.json"

// BundlesDir holds the bundles fetched from the server, by the hexadecimal SHA-256 of their bytes.
const BundlesDir = home.CacheDir + "/bundles"

// desiredVersion is the format of DesiredFile this program reads and writes.
const desiredVersion = 1

var (
	// ErrSignedOut: the server no longer takes this computer's device token (it was signed out,
	// or the account was disabled). Sign in again.
	ErrSignedOut = errors.New("the server no longer accepts this computer's sign-in")
	// ErrUnreachable: the server could not be reached and there is no desired state kept from
	// an earlier sync to use instead.
	ErrUnreachable = errors.New("the server cannot be reached and no earlier desired state is kept")
	// ErrTooOld: the server needs a newer version of this program.
	ErrTooOld = errors.New("the server needs a newer version of this program")
)

// TooOldError is ErrTooOld with the versions.
type TooOldError struct{ Have, Min string }

func (e *TooOldError) Error() string {
	return fmt.Sprintf("the server needs version %s or newer; this is %s", e.Min, e.Have)
}
func (e *TooOldError) Is(target error) bool { return target == ErrTooOld }

// Org is what the server says about the organisation and the person besides the resources.
type Org struct {
	// ToolsDisabled are the tools nobody's computer writes to. The stricter side wins: a tool
	// turned off here or on the computer is not written to.
	ToolsDisabled []string
	// MCPMode is how MCP servers are written into tools by default ("single-entry", "direct").
	MCPMode string
	// MinVersion and LatestVersion are the command-line versions the server accepts and hands
	// out.
	MinVersion, LatestVersion string
	// User is the person's username.
	User string
	// Secrets are the personal secrets the resources need, and whether the person has set them.
	Secrets []Secret
}

// Secret is a personal secret a resource needs.
type Secret struct {
	Name       string
	Configured bool
}

// ServerState is a desired state from the server.
type ServerState struct {
	*State
	Org Org
	// Cached is set when the server could not be reached and this is the state kept from the
	// last time it could. Nothing may be removed because of it: what the server would say now
	// is unknown.
	Cached bool
	// FetchedAt is when the server said it.
	FetchedAt time.Time
	// Unreachable is why the server could not be reached, when Cached.
	Unreachable error
}

// kept is DesiredFile.
type kept struct {
	Schema    int             `json:"schema"`
	Server    string          `json:"server"`
	ETag      string          `json:"etag,omitempty"`
	FetchedAt time.Time       `json:"fetched_at"`
	State     json.RawMessage `json:"state"`
}

// readKept reads the desired state kept for server. ok is false when there is none for this
// server, or it cannot be used: either way there is nothing to fall back on.
func readKept(dir home.Dir, server string) (k kept, doc apiclient.DesiredState, ok bool) {
	data, err := os.ReadFile(dir.Join(DesiredFile))
	if err != nil {
		return k, doc, false
	}
	if json.Unmarshal(data, &k) != nil || k.Schema != desiredVersion || k.Server != server {
		return k, doc, false
	}
	if json.Unmarshal(k.State, &doc) != nil {
		return k, doc, false
	}
	return k, doc, true
}

func writeKept(dir home.Dir, k kept) error {
	if _, err := dir.EnsureSub(home.StateDir); err != nil {
		return err
	}
	data, err := json.MarshalIndent(k, "", "  ")
	if err != nil {
		return err
	}
	return home.WriteFileAtomic(dir.Join(DesiredFile), append(data, '\n'), home.FilePerm)
}

// FromServer asks the server what this computer should have and gets the bundles of the
// resources it does not have yet. The answer is kept (DesiredFile) for when the server cannot
// be reached: then the kept state is returned with Cached set, and the caller must not remove
// anything because of it. Without a kept state that is ErrUnreachable.
//
// The other failures: ErrSignedOut when the server refuses the device token, ErrTooOld (a
// *TooOldError) when the server needs a newer program, which holds for a kept state too, and
// the server's other answers as *apiclient.Error. A resource whose bundle cannot be had or read
// is Failed: what was written for it stays as it is.
func FromServer(ctx context.Context, s Server, o ServerOptions) (*ServerState, error) {
	prev, prevDoc, havePrev := readKept(o.Data, o.URL)
	params := &apiclient.GetDesiredStateParams{}
	if havePrev && prev.ETag != "" {
		etag := prev.ETag
		params.IfNoneMatch = &etag
	}
	resp, err := s.GetDesiredStateWithResponse(ctx, params)
	if err == nil {
		err = answer(resp.HTTPResponse, resp.Body)
	}
	if err != nil {
		if apiclient.Unreachable(err) {
			return cachedOr(ctx, s, o, havePrev, prev, prevDoc, err)
		}
		return nil, err
	}
	doc, etag, fetched := prevDoc, prev.ETag, prev.FetchedAt
	switch resp.StatusCode() {
	case http.StatusNotModified:
		if !havePrev {
			return nil, fmt.Errorf("the server answered 304 Not Modified to a request without an ETag")
		}
	default:
		if resp.JSON200 == nil {
			return nil, fmt.Errorf("the server's desired state cannot be read")
		}
		doc, fetched = *resp.JSON200, time.Now().UTC()
		etag = ""
		if resp.Headers200 != nil && resp.Headers200.ETag != nil {
			etag = *resp.Headers200.ETag
		}
	}
	st, err := fromDoc(ctx, s, o, doc, false)
	var unreachable *bundleUnreachable
	if errors.As(err, &unreachable) {
		// The server went away halfway: what it would say is not known any better than before.
		return cachedOr(ctx, s, o, havePrev, prev, prevDoc, unreachable.err)
	}
	if err != nil {
		return nil, err
	}
	st.FetchedAt = fetched
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	if err := writeKept(o.Data, kept{Schema: desiredVersion, Server: o.URL, ETag: etag, FetchedAt: fetched, State: raw}); err != nil {
		return nil, fmt.Errorf("keeping the desired state for when the server cannot be reached: %w", err)
	}
	return st, nil
}

// cachedOr is the kept state, when there is one, in place of the server's answer.
func cachedOr(ctx context.Context, s Server, o ServerOptions, havePrev bool, prev kept, prevDoc apiclient.DesiredState, why error) (*ServerState, error) {
	if !havePrev {
		return nil, fmt.Errorf("%w: %w", ErrUnreachable, why)
	}
	st, err := fromDoc(ctx, s, o, prevDoc, true)
	if err != nil {
		return nil, err
	}
	st.Cached, st.FetchedAt, st.Unreachable = true, prev.FetchedAt, why
	return st, nil
}

// answer is nil for a success or a 304, ErrSignedOut for a refused token and an
// *apiclient.Error for anything else.
func answer(r *http.Response, body []byte) error {
	if r != nil && r.StatusCode == http.StatusNotModified {
		return nil
	}
	err := apiclient.Check(r, body)
	var ae *apiclient.Error
	if errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
		return fmt.Errorf("%w: %w", ErrSignedOut, err)
	}
	return err
}

// bundleUnreachable: a bundle could not be fetched because the server could not be reached.
type bundleUnreachable struct{ err error }

func (e *bundleUnreachable) Error() string { return e.err.Error() }

// fromDoc turns the server's document into a desired state. offline: the bundles may only come
// from the local cache.
func fromDoc(ctx context.Context, s Server, o ServerOptions, doc apiclient.DesiredState, offline bool) (*ServerState, error) {
	if o.Version != "" && Older(o.Version, doc.Cli.MinVersion) {
		return nil, &TooOldError{Have: o.Version, Min: doc.Cli.MinVersion}
	}
	st := &ServerState{State: &State{Source: Source{Type: SourceServer, URL: o.URL}}, Org: Org{
		ToolsDisabled: slices.Clone(doc.Defaults.ToolsDisabled), MCPMode: string(doc.Defaults.McpMode),
		MinVersion: doc.Cli.MinVersion, LatestVersion: doc.Cli.LatestVersion, User: doc.User.Username,
	}}
	for _, sec := range doc.Secrets {
		st.Org.Secrets = append(st.Org.Secrets, Secret{Name: sec.Name, Configured: sec.Configured})
	}
	kinds := map[string]kind.Kind{}
	var all []kind.Kind
	if o.Kinds != nil {
		all = o.Kinds.All()
	} else {
		all = kind.All()
	}
	for _, k := range all {
		kinds[k.ID()] = k
	}
	for _, r := range doc.Resources {
		ref := resource.Ref{Kind: r.Kind, Code: r.Code, Version: r.Version}
		fail := func(code, msg string) {
			st.Failed = append(st.Failed, Failed{Ref: ref, Issues: []repoformat.Issue{{File: r.Code, Code: code, Message: msg}}})
		}
		k, ok := kinds[r.Kind]
		if !ok {
			fail("unknown_kind", fmt.Sprintf("this version of the program does not know the resource kind %q", r.Kind))
			continue
		}
		data, err := fetchBundle(ctx, s, o.Data, r.Bundle, offline)
		var unreachable *bundleUnreachable
		if errors.As(err, &unreachable) {
			return nil, err
		}
		if err != nil {
			fail("bundle_unavailable", err.Error())
			continue
		}
		files, err := bundle.Unpack(bytes.NewReader(data))
		if err != nil {
			fail("bundle_unreadable", err.Error())
			continue
		}
		spec, err := k.Parse(ref, files)
		if err != nil {
			fail("parse", err.Error())
			continue
		}
		res := Resource{Ref: ref, Meta: metaOf(r.Meta), Digest: resource.Digest(r.Digest), Kind: k, Spec: spec, Reason: string(r.Reason)}
		if r.RequiredBy != nil {
			res.RequiredBy = slices.Clone(*r.RequiredBy)
		}
		st.Resources = append(st.Resources, res)
	}
	slices.SortFunc(st.Resources, func(a, b Resource) int { return cmp.Compare(a.Ref.Code, b.Ref.Code) })
	slices.SortFunc(st.Failed, func(a, b Failed) int { return cmp.Compare(a.Ref.Code, b.Ref.Code) })
	return st, nil
}

// fetchBundle returns the bytes of the bundle at address: from the local cache when they are
// there and match the address, otherwise from the server (unless offline), checked against the
// address and kept in the cache.
func fetchBundle(ctx context.Context, s Server, dir home.Dir, address string, offline bool) ([]byte, error) {
	hexSum, ok := strings.CutPrefix(address, "sha256:")
	if !ok || !resource.Digest(address).Valid() {
		return nil, fmt.Errorf("the bundle address %q is not a SHA-256 digest", address)
	}
	path := dir.Join(filepath.FromSlash(BundlesDir), hexSum)
	if data, err := os.ReadFile(path); err == nil && sumOf(data) == hexSum {
		return data, nil
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if offline {
		return nil, fmt.Errorf("the bundle %s is not in %s", address, BundlesDir)
	}
	resp, err := s.GetBundleWithResponse(ctx, address)
	if err == nil {
		err = answer(resp.HTTPResponse, resp.Body)
	}
	if err != nil {
		if apiclient.Unreachable(err) {
			return nil, &bundleUnreachable{err: err}
		}
		return nil, err
	}
	if sumOf(resp.Body) != hexSum {
		return nil, fmt.Errorf("the bundle %s that came does not match its address", address)
	}
	if _, err := dir.EnsureSub(filepath.FromSlash(BundlesDir)); err != nil {
		return nil, err
	}
	if err := home.WriteFileAtomic(path, resp.Body, home.FilePerm); err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func sumOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// metaOf is the metadata in force, as the server sends it.
func metaOf(m apiclient.ResourceMeta) resource.Meta {
	out := resource.Meta{Name: m.Name}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	out.Summary, out.When, out.Try = str(m.Summary), str(m.When), str(m.Try)
	if m.Stages != nil {
		out.Stages = slices.Clone(*m.Stages)
	}
	if m.Audience != nil {
		out.Audience = resource.Audience(*m.Audience)
	}
	if m.Scope != nil {
		out.Scope = resource.Scope(*m.Scope)
	}
	if m.Policy != nil {
		out.Policy = resource.Policy(*m.Policy)
	}
	if m.Secrets != nil {
		out.Secrets = slices.Clone(*m.Secrets)
	}
	if m.Maintainers != nil {
		out.Maintainers = slices.Clone(*m.Maintainers)
	}
	if m.Tags != nil {
		out.Tags = slices.Clone(*m.Tags)
	}
	if m.I18n != nil {
		out.I18n = map[string]resource.Text{}
		for lang, t := range *m.I18n {
			out.I18n[lang] = resource.Text{Name: str(t.Name), Summary: str(t.Summary), When: str(t.When), Try: str(t.Try)}
		}
	}
	return out
}

// Older reports whether version is a release older than need. A development build, or a version
// that cannot be read, is never older: it may be ahead of every release. (The same rule as
// requires.rgd in a repository, repoformat.)
func Older(version, need string) bool {
	have, ok := parseVersion(version)
	if !ok {
		return false
	}
	want, ok := parseVersion(need)
	if !ok {
		return false
	}
	for i := range have {
		if have[i] != want[i] {
			return have[i] < want[i]
		}
	}
	return false
}

// parseVersion reads "1.2.3"; a pre-release or build suffix makes it unreadable, which is what
// development builds ("0.0.0-dev+…") are.
func parseVersion(s string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

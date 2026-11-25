// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package sync

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/bundle"
	"github.com/code2rich/rigdepot/internal/desired"
	"github.com/code2rich/rigdepot/internal/flow"
	"github.com/code2rich/rigdepot/internal/gitrepo"
	"github.com/code2rich/rigdepot/internal/home"
)

// This file is what a sync keeps in the local data directory, besides the manifest, for the
// command line to use later: the steps of every workflow (rgd flow next) and the content of every
// knowledge base, fetched from Git with the person's own access. Nothing of it is written into a
// tool, so it needs no plan, no preview and no consent; nothing of it passes through a server.

// Fetcher takes a snapshot of a directory of a Git repository: what a knowledge base needs of a
// *gitrepo.Fetcher. It is the person's own git, with their credentials.
type Fetcher interface {
	Snapshot(ctx context.Context, src gitrepo.Source) (*gitrepo.Snapshot, error)
}

// Local is what KeepLocal did.
type Local struct {
	// Flows are the workflows written to and removed from flows/.
	Flows flow.Kept
	// Knowledge has one entry per knowledge base the desired state delivers, by code.
	Knowledge []Pull
	// KnowledgeRemoved are the knowledge bases removed because they are no longer delivered.
	KnowledgeRemoved []string
}

// Failed reports whether a knowledge base could not be fetched.
func (l *Local) Failed() bool {
	return slices.ContainsFunc(l.Knowledge, func(p Pull) bool { return p.Err != nil })
}

// Pull is one knowledge base fetched, or not.
type Pull struct {
	Resource resource.Ref
	Source   kind.GitSource
	// Path is where its content is, relative to the local data directory: knowledge/<code>.
	Path string
	// Commit is the commit fetched, and Files how many files were written.
	Commit string
	Files  int
	// Skipped are paths of the repository that were not written: symbolic links, and names this
	// system cannot have.
	Skipped []string
	// Err says why it could not be fetched; what was there from an earlier sync stays.
	Err error
}

// knowledgeMarker is the file at the top of a knowledge base's directory that says where its
// content came from.
const knowledgeMarker = bundle.MarkerFile

// KeepLocal writes the workflows of st to flows/ and fetches every knowledge base st delivers
// into knowledge/<code>/, replacing what an earlier sync fetched. What st no longer delivers is
// removed; what st could not read (st.Failed) stays as it was, since its state is unknown. A
// knowledge base that cannot be fetched keeps its earlier copy and is reported in its Pull.
//
// fetch is only used when st delivers a knowledge base; nil then fails each of them.
func (s *Session) KeepLocal(ctx context.Context, st *desired.State, fetch Fetcher) (*Local, error) {
	e := s.e
	out := &Local{}
	var err error
	if out.Flows, err = flow.Keep(e.data, st); err != nil {
		return out, fmt.Errorf("keeping the workflows: %w", err)
	}

	keep := map[string]bool{}
	for _, f := range st.Failed {
		keep[f.Ref.Code] = true
	}
	for _, r := range st.Resources {
		p, ok := r.Spec.(kind.Pulled)
		if !ok {
			continue
		}
		keep[r.Ref.Code] = true
		pull := Pull{Resource: r.Ref, Source: p.Pull(), Path: home.KnowledgeDir + "/" + r.Ref.Code}
		if fetch == nil {
			pull.Err = errors.New("there is no git to fetch it with")
		} else {
			pull.Err = e.fetchKnowledge(ctx, fetch, &pull)
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
		out.Knowledge = append(out.Knowledge, pull)
	}

	dir := e.data.Join(home.KnowledgeDir)
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return out, err
	}
	for _, en := range entries {
		code := en.Name()
		if !en.IsDir() || keep[code] || !resource.ValidCode(code) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, code)); err != nil {
			return out, err
		}
		out.KnowledgeRemoved = append(out.KnowledgeRemoved, code)
	}
	return out, nil
}

// fetchKnowledge fetches one knowledge base and puts it in place of the earlier copy: the new
// content is written next to it under a temporary name and renamed into place, so that the
// directory is the old content or the new one, never a mix.
func (e *Engine) fetchKnowledge(ctx context.Context, fetch Fetcher, p *Pull) error {
	snap, err := fetch.Snapshot(ctx, gitrepo.Source{URL: p.Source.URL, Ref: p.Source.Ref, Dir: p.Source.Dir})
	if err != nil {
		return err
	}
	parent, err := e.data.EnsureSub(home.KnowledgeDir)
	if err != nil {
		return err
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	tmp := filepath.Join(parent, ".tmp-"+p.Resource.Code+"-"+hex.EncodeToString(suffix))
	old := filepath.Join(parent, ".old-"+p.Resource.Code+"-"+hex.EncodeToString(suffix))
	target := filepath.Join(parent, p.Resource.Code)
	defer os.RemoveAll(tmp)

	if err := os.Mkdir(tmp, home.DirPerm); err != nil {
		return err
	}
	for link := range snap.Links {
		p.Skipped = append(p.Skipped, link)
	}
	err = fs.WalkDir(snap.Files, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || name == "." {
			return err
		}
		if !safeName(name) || (name == knowledgeMarker) {
			p.Skipped = append(p.Skipped, name)
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		dst := filepath.Join(tmp, filepath.FromSlash(name))
		if d.IsDir() {
			return os.MkdirAll(dst, home.DirPerm)
		}
		if !d.Type().IsRegular() {
			p.Skipped = append(p.Skipped, name)
			return nil
		}
		data, err := fs.ReadFile(snap.Files, name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, home.FilePerm); err != nil {
			return err
		}
		p.Files++
		return nil
	})
	if err != nil {
		return err
	}
	marker, err := json.MarshalIndent(map[string]any{
		"note":     brand.ManagedMarker,
		"resource": p.Resource.String(),
		"repo":     p.Source.URL,
		"ref":      p.Source.Ref,
		"dir":      p.Source.Dir,
		"commit":   snap.Commit,
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, knowledgeMarker), append(marker, '\n'), home.FilePerm); err != nil {
		return err
	}

	hadOld := false
	if _, err := os.Lstat(target); err == nil {
		if err := home.Rename(target, old); err != nil {
			return err
		}
		hadOld = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := home.Rename(tmp, target); err != nil {
		if hadOld {
			if back := home.Rename(old, target); back != nil {
				return errors.Join(err, fmt.Errorf("the earlier copy is at %s: %w", old, back))
			}
		}
		return err
	}
	if hadOld {
		os.RemoveAll(old)
	}
	slices.Sort(p.Skipped)
	p.Commit = snap.Commit
	return nil
}

// safeName reports whether a path of a fetched repository can be written as it is on every
// system: relative, no element that is empty, "." or "..", no character Windows refuses, no
// name Windows reserves.
func safeName(p string) bool {
	if !fs.ValidPath(p) || strings.ContainsAny(p, "\\:*?\"<>|") {
		return false
	}
	for _, el := range strings.Split(p, "/") {
		if strings.HasSuffix(el, ".") || strings.HasSuffix(el, " ") {
			return false
		}
		stem := strings.ToUpper(strings.TrimSuffix(el, path.Ext(el)))
		switch stem {
		case "CON", "PRN", "AUX", "NUL":
			return false
		}
		if len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '0' && stem[3] <= '9' {
			return false
		}
		for _, r := range el {
			if r < 0x20 {
				return false
			}
		}
	}
	return true
}

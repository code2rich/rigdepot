// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package desired

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"

	"github.com/code2rich/rigdepot/internal/bundle"
	"github.com/code2rich/rigdepot/internal/gitrepo"
	"github.com/code2rich/rigdepot/internal/repoformat"
)

// Archiver is what ReadRepo needs of a *gitrepo.Fetcher: the content of one commit as a tar.
type Archiver interface {
	Archive(ctx context.Context, src gitrepo.Source, w io.Writer) (commit string, err error)
}

// ReadRepo fetches a resource repository and reads it: git archive of the commit src points at,
// bundle.ReadTree of the archive, repoformat.Read of the tree. Reading the archive rather than a
// work tree makes the content, and so every digest, independent of how the person's Git converts
// line endings.
//
// The errors are those of the three steps: gitrepo's (ErrFetch, ErrNoRef … — the repository could
// not be had, and nothing may be removed because of it), bundle.ErrTreeTooLarge, and the errors
// of repoformat.Read for a repository that cannot be read as a whole.
func ReadRepo(ctx context.Context, a Archiver, src gitrepo.Source, opt repoformat.Options) (*repoformat.Repo, Source, error) {
	var archive bytes.Buffer
	commit, err := a.Archive(ctx, src, &archive)
	if err != nil {
		return nil, Source{}, err
	}
	tree, err := bundle.ReadTree(&archive)
	if err != nil {
		return nil, Source{}, fmt.Errorf("reading the repository at %s: %w", commit, err)
	}
	repo, err := repoformat.Read(tree, opt)
	if err != nil {
		return nil, Source{}, err
	}
	return repo, Source{Type: SourceRepo, URL: withoutPassword(src.URL), Ref: src.Ref, Dir: src.Dir, Commit: commit}, nil
}

// withoutPassword drops a password from an address, which is recorded in the manifest and shown.
func withoutPassword(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	if _, set := u.User.Password(); !set {
		return raw
	}
	u.User = url.User(u.User.Username())
	return u.String()
}

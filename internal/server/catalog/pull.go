// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/code2rich/rigdepot/internal/gitrepo"
	"github.com/code2rich/rigdepot/internal/repoformat"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/masterkey"
)

// SourceSyncJob is the name of the background job that pulls the sources (PullSources).
const SourceSyncJob = "source-sync"

// pullTarget is what a pull needs of a source row.
type pullTarget struct {
	id, name, url, branch, dir, credential string
}

// PullSources is the source-sync job: it pulls every source that is due (not pulled for half
// the interval, or never) or that someone asked to pull, in priority order, and imports what
// it read. Requests made while it runs are served before it returns.
//
// A failed pull is recorded on the source and changes nothing else: the catalog keeps what the
// last good pull imported, and nothing is ever deleted because a repository could not be read.
// PullSources returns an error naming the sources that failed, so that the job's result shows
// it, and the database errors it met.
func (s *Service) PullSources(ctx context.Context, f Fetcher) error {
	var failed []string
	var errs []error
	// The first pass pulls what is due; the next ones what was asked for while it ran.
	for pass := 0; pass < 3; pass++ {
		due, err := s.dueSources(ctx, pass == 0)
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		if len(due) == 0 {
			break
		}
		for _, t := range due {
			if err := ctx.Err(); err != nil {
				return errors.Join(append(errs, err)...)
			}
			ok, err := s.pull(ctx, f, t)
			if err != nil {
				errs = append(errs, fmt.Errorf("source %s: %w", t.name, err))
			} else if !ok {
				failed = append(failed, t.name)
			}
		}
	}
	if len(failed) > 0 {
		errs = append(errs, fmt.Errorf("catalog: could not pull %s", strings.Join(failed, ", ")))
	}
	return errors.Join(errs...)
}

// dueSources returns the sources to pull now: with all, the ones not pulled for half the
// interval and the ones asked for; otherwise only the ones asked for.
func (s *Service) dueSources(ctx context.Context, all bool) ([]pullTarget, error) {
	q := `SELECT id, name, url, branch, dir, credential FROM sources WHERE enabled = TRUE AND (pull_requested_at IS NOT NULL`
	var args []any
	if all {
		q += ` OR last_fetch_at IS NULL OR last_fetch_at <= ?`
		args = append(args, s.millis()-SourcePullEvery.Milliseconds()/2)
	}
	q += `) ORDER BY position, created_at, id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pullTarget
	for rows.Next() {
		var t pullTarget
		if err := rows.Scan(&t.id, &t.name, &t.url, &t.branch, &t.dir, &t.credential); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// pull pulls one source and imports it. ok is false when the pull failed and was recorded as
// failed; err is a database error.
func (s *Service) pull(ctx context.Context, f Fetcher, t pullTarget) (ok bool, err error) {
	start := s.millis()
	// A request made from now on asks for another pull.
	if _, err := s.db.ExecContext(ctx, `UPDATE sources SET pull_started_at = ?, pull_requested_at = NULL WHERE id = ?`, start, t.id); err != nil {
		return false, err
	}
	token := ""
	var why error
	if t.credential != "" {
		token, why = s.openCredential(t.id, t.credential)
	}
	var commit string
	var res ImportResult
	if why == nil {
		commit, res, why = s.fetchAndImport(ctx, f, t, token)
	}
	if ctx.Err() != nil && why != nil {
		// Stopped (shutdown, the job's timeout): not the repository's fault. The source asks
		// for a pull again so that the next run does it.
		_, err := s.db.ExecContext(context.WithoutCancel(ctx), `UPDATE sources SET pull_started_at = NULL, pull_requested_at = COALESCE(pull_requested_at, ?) WHERE id = ?`, start, t.id)
		return false, errors.Join(ctx.Err(), err)
	}
	var dbErr error
	if why != nil && !isPullFailure(why) {
		// The database or another fault of ours: record it on the source as well, so that the
		// page does not show the pull as running, and report it.
		dbErr = why
	}
	end := s.millis()
	if why != nil {
		_, err := s.db.ExecContext(context.WithoutCancel(ctx), `UPDATE sources SET pull_started_at = NULL, last_fetch_at = ?, last_fetch_ok = FALSE, last_error = ?
			WHERE id = ?`, end, pullError(why, token), t.id)
		return false, errors.Join(dbErr, err)
	}
	_, err = s.db.ExecContext(context.WithoutCancel(ctx), `UPDATE sources SET pull_started_at = NULL, last_fetch_at = ?, last_fetch_ok = TRUE, last_error = '',
		last_commit = ?, last_success_at = ?, last_imported = ?, last_failed = ? WHERE id = ?`,
		end, commit, end, len(res.Imported), len(res.Failed), t.id)
	return err == nil, err
}

// pullFailure is a pull that failed because of the repository or its content: recorded on the
// source, not an error of the server.
type pullFailure struct{ err error }

func (p pullFailure) Error() string { return p.err.Error() }
func (p pullFailure) Unwrap() error { return p.err }

func isPullFailure(err error) bool {
	var p pullFailure
	return errors.As(err, &p)
}

// fetchAndImport reads the source's commit and imports it.
func (s *Service) fetchAndImport(ctx context.Context, f Fetcher, t pullTarget, token string) (string, ImportResult, error) {
	src := gitrepo.Source{URL: t.url, Ref: t.branch, Dir: t.dir}
	if token != "" {
		src.Credential = &gitrepo.Credential{Password: token}
	}
	snap, err := f.Snapshot(ctx, src)
	if err != nil {
		return "", ImportResult{}, pullFailure{err}
	}
	repo, err := repoformat.Read(snap.Files, repoformat.Options{Kinds: s.kinds, Version: s.ver})
	if err != nil {
		return snap.Commit, ImportResult{}, pullFailure{fmt.Errorf("commit %s: %w", shortCommit(snap.Commit), err)}
	}
	res, err := s.ImportRepo(ctx, access.System, ImportSource{SourceID: t.id, Commit: snap.Commit}, repo)
	if err != nil {
		return snap.Commit, res, err
	}
	return snap.Commit, res, nil
}

// openCredential opens a source's sealed credential.
func (s *Service) openCredential(id, stored string) (string, error) {
	if s.keys == nil {
		return "", ErrNoMasterKey
	}
	sealed, err := masterkey.Decode(stored)
	if err != nil {
		return "", fmt.Errorf("catalog: credential of source %s: %w", id, err)
	}
	plain, err := s.keys.Open(sealed, credentialAD(id))
	if err != nil {
		return "", fmt.Errorf("catalog: credential of source %s: %w", id, err)
	}
	return string(plain), nil
}

// maxPullError is how much of a failure is kept for the page.
const maxPullError = 1000

// pullError is the text kept for a failed pull: the credential masked (git does not print it,
// but a proxy's error page might echo it), and cut to a length a page can show.
func pullError(err error, token string) string {
	msg := err.Error()
	if token != "" {
		msg = strings.ReplaceAll(msg, token, "xxxxx")
	}
	if utf8.RuneCountInString(msg) > maxPullError {
		r := []rune(msg)
		msg = string(r[:maxPullError]) + "…"
	}
	return msg
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

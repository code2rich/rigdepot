// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/code2rich/rigdepot/internal/gitrepo"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/masterkey"
)

// SourcePullEvery is how often every resource repository is pulled.
const SourcePullEvery = 10 * time.Minute

// pullRunningFor is how long a pull that started counts as running. A server that stopped
// in the middle of a pull leaves pull_started_at behind; after this long the source shows its
// last finished pull again.
const pullRunningFor = time.Hour

// PullResult is how a source's last pull went.
type PullResult string

// The results of a pull, as in the contract's SourcePull.
const (
	PullOK      PullResult = "ok"
	PullFailed  PullResult = "failed"
	PullRunning PullResult = "running" // asked for or in progress
)

// Source is a resource repository the catalog imports from.
type Source struct {
	ID     string
	Name   string
	URL    string
	Branch string // "" is the repository's default branch
	Dir    string // the directory holding rigdepot.yaml; "" for the root
	// Order is the 1-based position: when two repositories have a resource with the same code,
	// the earlier one is pulled first and keeps it.
	Order         int
	HasCredential bool
	ResourceCount int // resources imported from it
	Revision      int64
	LastPull      *Pull // nil before the first pull is asked for
}

// ETag is the source's entity tag for If-Match.
func (s Source) ETag() string { return `"s-` + s.ID + "-" + strconv.FormatInt(s.Revision, 10) + `"` }

// Pull is a source's last pull, or the one running now.
type Pull struct {
	At            time.Time // when it finished, or when the running one was asked for or started
	Result        PullResult
	Commit        string // the commit of the last successful pull
	Imported      int    // drafts the last successful pull created or updated
	Failed        int    // resources it could not import
	Error         string // why the last pull failed, credentials masked
	LastSuccessAt time.Time
}

// SourceInput is a new source.
type SourceInput struct {
	Name, URL, Branch, Dir string
	// Credential is a read-only token for an https repository; "" for none. It is sealed with
	// the master key and never returned.
	Credential string
}

// SourceChange changes a source; nil fields stay.
type SourceChange struct {
	Name, URL, Branch, Dir *string
	Credential             *string
	ClearCredential        bool
}

// Fetcher takes a snapshot of a repository. *gitrepo.Fetcher implements it.
type Fetcher interface {
	Snapshot(ctx context.Context, src gitrepo.Source) (*gitrepo.Snapshot, error)
}

// Errors of sources.
var (
	// ErrNoMasterKey: a credential cannot be stored or used without the master key.
	ErrNoMasterKey = errors.New("catalog: no master key to seal source credentials with")
)

// credentialAD binds a sealed credential to its source: copied to another row, it does not open.
func credentialAD(id string) []byte { return []byte("sources/" + id + "/credential") }

func sourceObject(id, name string) audit.Object {
	return audit.Object{Kind: "source", ID: id, Label: name}
}

const sourceCols = `s.id, s.name, s.url, s.branch, s.dir, s.credential <> '', s.position, s.revision,
	s.last_fetch_at, s.last_fetch_ok, s.last_error, s.last_commit, s.last_success_at, s.last_imported, s.last_failed,
	s.pull_requested_at, s.pull_started_at,
	(SELECT COUNT(*) FROM resources r WHERE r.source_id = s.id)`

func (s *Service) scanSource(sc interface{ Scan(...any) error }) (Source, error) {
	var (
		src                                  Source
		fetchAt, success, requested, started sql.NullInt64
		fetchOK                              sql.NullBool
		lastErr, commit                      string
		imported, failed                     int
	)
	err := sc.Scan(&src.ID, &src.Name, &src.URL, &src.Branch, &src.Dir, &src.HasCredential, &src.Order, &src.Revision,
		&fetchAt, &fetchOK, &lastErr, &commit, &success, &imported, &failed, &requested, &started, &src.ResourceCount)
	if err != nil {
		return src, err
	}
	now := s.now()
	switch {
	case started.Valid && dbx.Time(started.Int64).After(now.Add(-pullRunningFor)):
		src.LastPull = &Pull{At: dbx.Time(started.Int64), Result: PullRunning}
	case requested.Valid:
		src.LastPull = &Pull{At: dbx.Time(requested.Int64), Result: PullRunning}
	case fetchAt.Valid:
		src.LastPull = &Pull{At: dbx.Time(fetchAt.Int64), Result: PullFailed, Error: lastErr}
		if fetchOK.Valid && fetchOK.Bool {
			src.LastPull.Result, src.LastPull.Error = PullOK, ""
		}
	}
	if src.LastPull != nil {
		if commit != "" {
			src.LastPull.Commit, src.LastPull.Imported, src.LastPull.Failed = commit, imported, failed
		}
		if success.Valid {
			src.LastPull.LastSuccessAt = dbx.Time(success.Int64)
		}
	}
	return src, nil
}

func (s *Service) loadSources(ctx context.Context, q dbx.Querier, where string, args ...any) ([]Source, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+sourceCols+` FROM sources s `+where+` ORDER BY s.position, s.created_at, s.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		src, err := s.scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

func (s *Service) loadSource(ctx context.Context, q dbx.Querier, id string) (Source, error) {
	srcs, err := s.loadSources(ctx, q, `WHERE s.id = ?`, id)
	if err != nil {
		return Source{}, err
	}
	if len(srcs) == 0 {
		return Source{}, ErrNotFound
	}
	return srcs[0], nil
}

// Sources returns the resource repositories in priority order. Only platform administrators
// see them: the addresses can be internal.
func (s *Service) Sources(ctx context.Context, a access.Actor) ([]Source, error) {
	if err := s.can(ctx, a, access.ActionSourceManage, access.TargetPlatform()); err != nil {
		return nil, err
	}
	return s.loadSources(ctx, s.db, "")
}

// Source returns one resource repository.
func (s *Service) Source(ctx context.Context, a access.Actor, id string) (Source, error) {
	if err := s.can(ctx, a, access.ActionSourceManage, access.TargetPlatform()); err != nil {
		return Source{}, err
	}
	return s.loadSource(ctx, s.db, id)
}

// validSource checks a source's fields as they will be stored. The address is checked the way
// the pull will check it, so a source that cannot ever be pulled is refused at once.
func (s *Service) validSource(name, rawURL, branch, dir string, credential bool) error {
	var fields []FieldError
	tooLong := func(field, v string, most int) {
		if utf8.RuneCountInString(v) > most {
			fields = append(fields, FieldError{Field: field, Code: "too_long"})
		}
	}
	if strings.TrimSpace(name) == "" {
		fields = append(fields, FieldError{Field: "name", Code: "required"})
	}
	tooLong("name", name, 64)
	tooLong("url", rawURL, 1024)
	tooLong("branch", branch, 255)
	tooLong("dir", dir, 255)
	transport, err := gitrepo.CheckAddress(rawURL, s.allowLocal)
	switch {
	case rawURL == "":
		fields = append(fields, FieldError{Field: "url", Code: "required"})
	case err != nil:
		fields = append(fields, FieldError{Field: "url", Code: "invalid", Detail: "an https or ssh Git address"})
	case hasPassword(rawURL):
		// It would be shown to every administrator; a token goes into credential.
		fields = append(fields, FieldError{Field: "url", Code: "has_password", Detail: "put the token into credential"})
	case credential && transport != "https":
		fields = append(fields, FieldError{Field: "credential", Code: "unsupported",
			Detail: "a token is only sent to https addresses; ssh uses the server's own keys"})
	}
	if strings.TrimSpace(branch) == "" {
		fields = append(fields, FieldError{Field: "branch", Code: "required"})
	} else if strings.HasPrefix(branch, "-") || strings.ContainsFunc(branch, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		fields = append(fields, FieldError{Field: "branch", Code: "invalid"})
	}
	if d := strings.Trim(dir, "/"); d != "" && (!fs.ValidPath(d) || d == ".") {
		fields = append(fields, FieldError{Field: "dir", Code: "invalid", Detail: "a relative, slash-separated directory"})
	}
	if len(fields) > 0 {
		return invalid(fields...)
	}
	return nil
}

func hasPassword(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return false
	}
	_, ok := u.User.Password()
	return ok
}

// seal seals a source's credential for storage.
func (s *Service) seal(id, credential string) (string, error) {
	if credential == "" {
		return "", nil
	}
	if s.keys == nil {
		return "", ErrNoMasterKey
	}
	sealed, err := s.keys.Seal([]byte(credential), credentialAD(id))
	if err != nil {
		return "", err
	}
	return sealed.Encode(), nil
}

// CreateSource adds a resource repository at the end of the order and asks for its first pull.
func (s *Service) CreateSource(ctx context.Context, a access.Actor, in SourceInput) (Source, error) {
	in.Name, in.Dir = strings.TrimSpace(in.Name), strings.Trim(in.Dir, "/")
	if err := s.validSource(in.Name, in.URL, in.Branch, in.Dir, in.Credential != ""); err != nil {
		return Source{}, err
	}
	id := dbx.NewID()
	sealed, err := s.seal(id, in.Credential)
	if err != nil {
		return Source{}, err
	}
	var out Source
	err = s.write(ctx, a, func(o *op) error {
		obj := sourceObject(id, in.Name)
		if err := o.check(access.ActionSourceManage, access.TargetPlatform(), obj); err != nil {
			return err
		}
		now := s.millis()
		if _, err := o.tx.ExecContext(ctx, `INSERT INTO sources (id, name, url, branch, dir, credential, position, pull_requested_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, (SELECT COALESCE(MAX(position), 0) + 1 FROM sources), ?, ?, ?)`,
			id, in.Name, in.URL, in.Branch, in.Dir, sealed, now, now, now); err != nil {
			return err
		}
		change := map[string]any{"url": redactURL(in.URL), "branch": in.Branch, "dir": in.Dir, "credential": in.Credential != ""}
		if err := o.record(access.ActionSourceManage, obj, map[string]any{"created": change}); err != nil {
			return err
		}
		o.tx.AfterCommit(s.pullNow)
		out, err = s.loadSource(ctx, o.tx, id)
		return err
	})
	return out, err
}

// UpdateSource changes a resource repository. ifMatch is the source's ETag the change is based
// on. A change of where the content comes from asks for a pull.
func (s *Service) UpdateSource(ctx context.Context, a access.Actor, id string, ifMatch *string, ch SourceChange) (Source, error) {
	var out Source
	err := s.write(ctx, a, func(o *op) error {
		cur, err := s.loadSource(ctx, o.tx, id)
		if errors.Is(err, ErrNotFound) {
			// Whoever may not manage sources does not learn which exist.
			if err := o.check(access.ActionSourceManage, access.TargetPlatform(), sourceObject(id, id)); err != nil {
				return err
			}
		}
		if err != nil {
			return err
		}
		obj := sourceObject(id, cur.Name)
		if err := o.check(access.ActionSourceManage, access.TargetPlatform(), obj); err != nil {
			return err
		}
		switch {
		case ifMatch == nil:
			return ErrPreconditionRequired
		case *ifMatch != cur.ETag():
			return ErrPreconditionFailed
		}
		next := cur
		set := func(dst *string, v *string) {
			if v != nil {
				*dst = *v
			}
		}
		set(&next.Name, ch.Name)
		set(&next.URL, ch.URL)
		set(&next.Branch, ch.Branch)
		set(&next.Dir, ch.Dir)
		next.Name, next.Dir = strings.TrimSpace(next.Name), strings.Trim(next.Dir, "/")
		credential := ch.Credential != nil && *ch.Credential != ""
		if ch.ClearCredential && credential {
			return invalid(FieldError{Field: "clear_credential", Code: "conflict", Detail: "give a credential or clear it, not both"})
		}
		keeps := cur.HasCredential && !ch.ClearCredential && !credential
		if err := s.validSource(next.Name, next.URL, next.Branch, next.Dir, credential || keeps); err != nil {
			return err
		}

		change := map[string]any{}
		for _, f := range []struct{ name, old, new string }{
			{"name", cur.Name, next.Name}, {"url", redactURL(cur.URL), redactURL(next.URL)},
			{"branch", cur.Branch, next.Branch}, {"dir", cur.Dir, next.Dir},
		} {
			if f.old != f.new {
				change[f.name] = map[string]any{"from": f.old, "to": f.new}
			}
		}
		q := `UPDATE sources SET name = ?, url = ?, branch = ?, dir = ?`
		args := []any{next.Name, next.URL, next.Branch, next.Dir}
		switch {
		case credential:
			sealed, err := s.seal(id, *ch.Credential)
			if err != nil {
				return err
			}
			q, args = q+`, credential = ?`, append(args, sealed)
			change["credential"] = "replaced"
		case ch.ClearCredential && cur.HasCredential:
			q += `, credential = ''`
			change["credential"] = "removed"
		}
		if len(change) == 0 {
			out = cur
			return nil
		}
		now := s.millis()
		if next.URL != cur.URL || next.Branch != cur.Branch || next.Dir != cur.Dir || change["credential"] != nil {
			q, args = q+`, pull_requested_at = ?`, append(args, now)
			o.tx.AfterCommit(s.pullNow)
		}
		q, args = q+`, revision = revision + 1, updated_at = ? WHERE id = ?`, append(args, now, id)
		if _, err := o.tx.ExecContext(ctx, q, args...); err != nil {
			return err
		}
		if err := o.record(access.ActionSourceManage, obj, change); err != nil {
			return err
		}
		out, err = s.loadSource(ctx, o.tx, id)
		return err
	})
	return out, err
}

// DeleteSource stops importing from a repository. The resources it brought keep their
// versions, drafts and settings; they get no new drafts until another source has them.
func (s *Service) DeleteSource(ctx context.Context, a access.Actor, id string) error {
	return s.write(ctx, a, func(o *op) error {
		cur, err := s.loadSource(ctx, o.tx, id)
		if errors.Is(err, ErrNotFound) {
			if err := o.check(access.ActionSourceManage, access.TargetPlatform(), sourceObject(id, id)); err != nil {
				return err
			}
		}
		if err != nil {
			return err
		}
		obj := sourceObject(id, cur.Name)
		if err := o.check(access.ActionSourceManage, access.TargetPlatform(), obj); err != nil {
			return err
		}
		if _, err := o.tx.ExecContext(ctx, `UPDATE resources SET source_id = NULL WHERE source_id = ?`, id); err != nil {
			return err
		}
		if _, err := o.tx.ExecContext(ctx, `DELETE FROM sources WHERE id = ?`, id); err != nil {
			return err
		}
		if _, err := o.tx.ExecContext(ctx, `UPDATE sources SET position = position - 1 WHERE position > ?`, cur.Order); err != nil {
			return err
		}
		return o.record(access.ActionSourceManage, obj, map[string]any{"deleted": map[string]any{"url": redactURL(cur.URL), "resources": cur.ResourceCount}})
	})
}

// OrderSources puts the sources in the order of ids, which names every source once.
func (s *Service) OrderSources(ctx context.Context, a access.Actor, ids []string) ([]Source, error) {
	var out []Source
	err := s.write(ctx, a, func(o *op) error {
		obj := audit.Object{Kind: "source", ID: "order", Label: "order"}
		if err := o.check(access.ActionSourceManage, access.TargetPlatform(), obj); err != nil {
			return err
		}
		cur, err := s.loadSources(ctx, o.tx, "")
		if err != nil {
			return err
		}
		known := map[string]bool{}
		for _, src := range cur {
			known[src.ID] = true
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if !known[id] || seen[id] {
				return invalid(FieldError{Field: "ids", Code: "mismatch", Detail: "name every source exactly once"})
			}
			seen[id] = true
		}
		if len(ids) != len(cur) {
			return invalid(FieldError{Field: "ids", Code: "mismatch", Detail: "name every source exactly once"})
		}
		changed := false
		for i, id := range ids {
			if cur[i].ID == id {
				continue
			}
			changed = true
			if _, err := o.tx.ExecContext(ctx, `UPDATE sources SET position = ?, revision = revision + 1, updated_at = ? WHERE id = ?`, i+1, s.millis(), id); err != nil {
				return err
			}
		}
		if changed {
			if err := o.record(access.ActionSourceManage, obj, map[string]any{"order": ids}); err != nil {
				return err
			}
		}
		out, err = s.loadSources(ctx, o.tx, "")
		return err
	})
	return out, err
}

// RequestPull asks for a pull of the source now. The pull job runs it, here or on another
// instance; the source shows it as running until it is done.
func (s *Service) RequestPull(ctx context.Context, a access.Actor, id string) (Source, error) {
	var out Source
	err := s.write(ctx, a, func(o *op) error {
		cur, err := s.loadSource(ctx, o.tx, id)
		if errors.Is(err, ErrNotFound) {
			if err := o.check(access.ActionSourceManage, access.TargetPlatform(), sourceObject(id, id)); err != nil {
				return err
			}
		}
		if err != nil {
			return err
		}
		obj := sourceObject(id, cur.Name)
		if err := o.check(access.ActionSourceManage, access.TargetPlatform(), obj); err != nil {
			return err
		}
		if _, err := o.tx.ExecContext(ctx, `UPDATE sources SET pull_requested_at = ? WHERE id = ?`, s.millis(), id); err != nil {
			return err
		}
		if err := o.record(access.ActionSourceManage, obj, map[string]any{"pull": "requested"}); err != nil {
			return err
		}
		o.tx.AfterCommit(s.pullNow)
		out, err = s.loadSource(ctx, o.tx, id)
		return err
	})
	return out, err
}

func (s *Service) pullNow() {
	if s.pullTrigger != nil {
		s.pullTrigger()
	}
}

// redactURL removes a password from an address for the audit log (validSource refuses one,
// but an address stored by an older version might have it).
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	if _, ok := u.User.Password(); !ok {
		return raw
	}
	u.User = url.UserPassword(u.User.Username(), "xxxxx")
	return u.String()
}

// SourcesFailing returns the names of the sources whose last pull failed, for the readiness
// check: the server stays ready, degraded.
func (s *Service) SourcesFailing(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM sources WHERE last_fetch_ok = FALSE ORDER BY position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// SourcesRewrapper moves every source credential to the current master key, for "keys rotate".
var SourcesRewrapper = masterkey.Rewrapper{Name: "sources", Rewrap: rewrapCredentials}

func rewrapCredentials(ctx context.Context, tx *dbx.Tx, keys *masterkey.Keyring) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, credential FROM sources WHERE credential <> '' ORDER BY id`)
	if err != nil {
		return 0, err
	}
	type sealedRow struct{ id, value string }
	var all []sealedRow
	for rows.Next() {
		var r sealedRow
		if err := rows.Scan(&r.id, &r.value); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, r := range all {
		sealed, err := masterkey.Decode(r.value)
		if err != nil {
			return n, fmt.Errorf("source %s: %w", r.id, err)
		}
		sealed, changed, err := keys.Rewrap(sealed)
		if err != nil {
			return n, fmt.Errorf("source %s: %w", r.id, err)
		}
		if !changed {
			continue
		}
		// revision stays: nobody changed the source.
		if _, err := tx.ExecContext(ctx, `UPDATE sources SET credential = ? WHERE id = ?`, sealed.Encode(), r.id); err != nil {
			return n, fmt.Errorf("source %s: %w", r.id, err)
		}
		n++
	}
	return n, nil
}

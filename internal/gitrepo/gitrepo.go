// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package gitrepo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
)

// The errors a caller tells apart with errors.Is. Each carries a sentence for the person in its
// message; the command line maps them to exit codes and translated text.
var (
	// ErrNoGit means the git program could not be found or started.
	ErrNoGit = errors.New("git was not found")
	// ErrAddress means the repository address was refused before git was run.
	ErrAddress = errors.New("repository address not allowed")
	// ErrFetch means git could not fetch from the repository: unreachable, no access, or not a
	// repository. Nothing in the cache was removed.
	ErrFetch = errors.New("could not fetch the repository")
	// ErrNoRef means the repository has no branch, tag or commit by that name.
	ErrNoRef = errors.New("no such branch, tag or commit")
	// ErrNoDir means the commit has no directory by that name.
	ErrNoDir = errors.New("no such directory in the commit")
	// ErrTimeout means a git command did not finish in time and was stopped.
	ErrTimeout = errors.New("git did not finish in time")
	// ErrTooLarge means the content is larger than Options.MaxBytes.
	ErrTooLarge = errors.New("repository content too large")
)

// DefaultTimeout is how long one git command may run when Options.Timeout is zero.
const DefaultTimeout = 5 * time.Minute

// DefaultMaxBytes is the most content one snapshot may hold when Options.MaxBytes is zero.
const DefaultMaxBytes = 256 << 20

// Options configures a Fetcher.
type Options struct {
	// Git is the git program: a path, or "" to look for "git" on PATH. The command line fills it
	// from its configuration (the RGD_GIT variable).
	Git string
	// CacheDir holds the bare clones, one directory per address. It is created when missing.
	CacheDir string
	// Timeout limits each git command. Zero means DefaultTimeout.
	Timeout time.Duration
	// AllowLocal accepts local paths and file:// addresses. Set it only when the person typed
	// the address themselves; a server reading addresses others entered leaves it off, so that
	// nobody can make it read its own disk.
	AllowLocal bool
	// MaxBytes limits the size of one snapshot as git archive writes it. Zero means
	// DefaultMaxBytes.
	MaxBytes int64
}

// Source says what to take a snapshot of.
type Source struct {
	// URL is the repository address: https, ssh (URL or scp-like "host:path"), or with
	// Options.AllowLocal a local path or file:// URL.
	URL string
	// Ref is a branch, a tag, a full reference ("refs/heads/main") or a full commit id. Empty
	// means the branch the repository's HEAD points at.
	Ref string
	// Dir is a directory inside the repository, slash-separated, whose content becomes the root
	// of the snapshot. Empty means the whole repository.
	Dir string
	// Credential, when set, is sent to an https repository; any other address with a
	// credential is refused with ErrAddress.
	Credential *Credential
}

// Snapshot is the content of one commit.
type Snapshot struct {
	// Commit is the full id of the commit the reference pointed at.
	Commit string
	// Files is the content: regular files and directories, read-only and in memory. Executable
	// files have mode 0555, others 0444. The bytes are the bytes stored in the commit, with no
	// line-ending or attribute conversion.
	Files fs.FS
	// Links lists the symbolic links in the content, by slash-separated path, with their targets.
	// They are not part of Files; a reader that refuses links checks here.
	Links map[string]string
}

// Fetcher takes snapshots of repositories.
type Fetcher struct {
	git        string
	cacheDir   string
	timeout    time.Duration
	allowLocal bool
	maxBytes   int64
}

// New returns a Fetcher. It fails with ErrNoGit when the git program cannot be found, so that
// the person hears about it before anything else happens.
func New(opts Options) (*Fetcher, error) {
	if opts.CacheDir == "" {
		return nil, errors.New("gitrepo: no cache directory given")
	}
	f := &Fetcher{
		git:        opts.Git,
		cacheDir:   opts.CacheDir,
		timeout:    opts.Timeout,
		allowLocal: opts.AllowLocal,
		maxBytes:   opts.MaxBytes,
	}
	if f.timeout <= 0 {
		f.timeout = DefaultTimeout
	}
	if f.maxBytes <= 0 {
		f.maxBytes = DefaultMaxBytes
	}
	name := opts.Git
	if name == "" {
		name = "git"
	}
	p, err := exec.LookPath(name)
	if err != nil {
		f.git = opts.Git
		return nil, f.notFound()
	}
	f.git = p
	return f, nil
}

// notFound is the error for a git that is not there, saying what to do about it.
func (f *Fetcher) notFound() error {
	if f.git == "" {
		return fmt.Errorf("%w: reading a repository directly needs Git, and no git program was found on PATH; install Git, or set %sGIT to the path of the git program", ErrNoGit, brand.EnvPrefix)
	}
	return fmt.Errorf("%w: the git program %q does not exist or cannot be run; install Git, or set %sGIT to the path of the git program", ErrNoGit, f.git, brand.EnvPrefix)
}

// Version returns what "git version" prints, for diagnostics.
func (f *Fetcher) Version(ctx context.Context) (string, error) {
	out, err := f.run(ctx, command{args: []string{"version"}})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Snapshot fetches the repository into the cache and returns the content of the commit the
// reference points at.
func (f *Fetcher) Snapshot(ctx context.Context, src Source) (*Snapshot, error) {
	var archive bytes.Buffer
	commit, err := f.Archive(ctx, src, &archive)
	if err != nil {
		return nil, err
	}
	files, links, err := readTar(&archive)
	if err != nil {
		return nil, err
	}
	return &Snapshot{Commit: commit, Files: files, Links: links}, nil
}

// Archive is Snapshot for a caller that reads the content itself: it fetches the repository and
// writes the content of the commit to w as the tar stream git archive produces (paths relative
// to Source.Dir, directories included, symbolic links as link entries, the executable bit in the
// mode, a pax global header with the commit id first), and returns the commit id. The bytes are
// the stored bytes, as for Snapshot.
//
// On an error, what was already written to w is incomplete and has to be discarded.
func (f *Fetcher) Archive(ctx context.Context, src Source, w io.Writer) (commit string, err error) {
	addr, err := parseAddress(src.URL, f.allowLocal)
	if err != nil {
		return "", err
	}
	if err := checkRef(src.Ref); err != nil {
		return "", err
	}
	auth, err := authConfig(addr, src.Credential)
	if err != nil {
		return "", err
	}
	dir := strings.Trim(src.Dir, "/")
	if dir != "" && (!fs.ValidPath(dir) || dir == ".") {
		return "", fmt.Errorf("%w: %q is not a relative, slash-separated directory", ErrNoDir, src.Dir)
	}

	repo, err := f.repository(ctx, addr)
	if err != nil {
		return "", err
	}
	if err := f.fetch(ctx, repo, addr, src.Ref == "", auth); err != nil {
		return "", err
	}
	commit, err = f.resolve(ctx, repo, src.Ref)
	if err != nil {
		return "", err
	}
	treeish := commit
	if dir != "" {
		out, err := f.run(ctx, command{gitDir: repo, args: []string{"cat-file", "-t", commit + ":" + dir}})
		if err != nil || strings.TrimSpace(string(out)) != "tree" {
			return "", fmt.Errorf("%w: %q in commit %s", ErrNoDir, dir, commit)
		}
		treeish = commit + ":" + dir
	}

	capped := &cappedWriter{w: w, max: f.maxBytes}
	if _, err := f.run(ctx, command{gitDir: repo, args: []string{"archive", "--format=tar", treeish}, stdout: capped}); err != nil {
		if capped.over {
			return "", fmt.Errorf("%w: more than %d bytes", ErrTooLarge, f.maxBytes)
		}
		return "", err
	}
	return commit, nil
}

// headRef is where the commit the remote HEAD points at is kept in the cache repository.
const headRef = "refs/upstream/HEAD"

// rawAttributes switches off every conversion git archive would otherwise apply. In
// info/attributes it takes precedence over the .gitattributes files of the repository.
const rawAttributes = "* -text -eol -crlf -ident -filter -working-tree-encoding -export-subst -export-ignore\n"

// repository returns the cache repository for an address, creating it when it does not exist or
// is not a usable repository.
func (f *Fetcher) repository(ctx context.Context, addr address) (string, error) {
	if err := os.MkdirAll(f.cacheDir, 0o700); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(addr.arg))
	repo := filepath.Join(f.cacheDir, hex.EncodeToString(sum[:16]))

	if _, err := os.Stat(repo); err == nil {
		out, err := f.run(ctx, command{gitDir: repo, args: []string{"rev-parse", "--is-bare-repository"}})
		var ge *gitError
		switch {
		case err == nil && strings.TrimSpace(string(out)) == "true":
			return repo, writeAttributes(repo)
		case err != nil && !errors.As(err, &ge):
			return "", err // git itself could not run: that says nothing about the cache
		}
		// Not a repository any more (an interrupted earlier version, a disk problem): it is
		// only a cache, so start over.
		if err := os.RemoveAll(repo); err != nil {
			return "", err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}

	tmp, err := os.MkdirTemp(f.cacheDir, ".new-")
	if err != nil {
		return "", err
	}
	if _, err := f.run(ctx, command{args: []string{"init", "--quiet", "--bare", "--template=", tmp}}); err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	if err := writeAttributes(tmp); err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	if err := os.Rename(tmp, repo); err != nil {
		os.RemoveAll(tmp)
		if _, statErr := os.Stat(repo); statErr == nil {
			return repo, nil // another process created it in the meantime
		}
		return "", err
	}
	return repo, nil
}

func writeAttributes(repo string) error {
	file := filepath.Join(repo, "info", "attributes")
	if data, err := os.ReadFile(file); err == nil && string(data) == rawAttributes {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	return os.WriteFile(file, []byte(rawAttributes), 0o600)
}

// fetch brings the cache repository up to date: every branch and tag, and with head also the
// commit HEAD points at. HEAD is only asked for when it is needed, because a repository whose
// HEAD names a branch that does not exist would otherwise fail every fetch. The address is passed on the command line instead of being stored as a remote, so
// the cache never holds anything but objects and references.
func (f *Fetcher) fetch(ctx context.Context, repo string, addr address, head bool, auth [][2]string) error {
	args := []string{
		"fetch", "--quiet", "--prune", "--force", "--no-tags", "--no-recurse-submodules",
		"--", addr.arg,
		"+refs/heads/*:refs/heads/*",
		"+refs/tags/*:refs/tags/*",
	}
	if head {
		args = append(args, "+HEAD:"+headRef)
	}
	if _, err := f.run(ctx, command{gitDir: repo, allow: addr.transport, args: args, shown: &addr, config: auth}); err != nil {
		var ge *gitError
		if errors.As(err, &ge) {
			return fmt.Errorf("%w %s: %s", ErrFetch, addr.shown, ge.msg)
		}
		return err
	}
	return nil
}

// resolve turns a reference into a commit id.
func (f *Fetcher) resolve(ctx context.Context, repo, ref string) (string, error) {
	var candidates []string
	switch {
	case ref == "":
		candidates = []string{headRef}
	case strings.HasPrefix(ref, "refs/"):
		candidates = []string{ref}
	case isCommitID(ref):
		candidates = []string{ref}
	default:
		candidates = []string{"refs/heads/" + ref, "refs/tags/" + ref}
	}
	for _, c := range candidates {
		out, err := f.run(ctx, command{gitDir: repo, args: []string{"rev-parse", "--verify", "--quiet", c + "^{commit}"}})
		if err == nil {
			return strings.TrimSpace(string(out)), nil
		}
		var ge *gitError
		if !errors.As(err, &ge) {
			return "", err
		}
	}
	if ref == "" {
		ref = "HEAD"
	}
	return "", fmt.Errorf("%w: %q", ErrNoRef, ref)
}

// checkRef refuses references that git could read as an option or as revision syntax.
func checkRef(ref string) error {
	if ref == "" {
		return nil
	}
	bad := strings.HasPrefix(ref, "-") || strings.HasPrefix(ref, "/") || strings.HasSuffix(ref, "/") ||
		strings.Contains(ref, "..") || strings.Contains(ref, "@{") || strings.Contains(ref, "//") ||
		strings.ContainsAny(ref, " ~^:?*[\\") ||
		strings.ContainsFunc(ref, func(r rune) bool { return r < 0x20 || r == 0x7f })
	if bad || path.Clean(ref) != ref {
		return fmt.Errorf("%w: %q is not a valid branch, tag or commit name", ErrNoRef, ref)
	}
	return nil
}

// isCommitID reports whether s is a full SHA-1 or SHA-256 object id.
func isCommitID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}

// cappedWriter passes up to max bytes on and fails the write that goes over, which stops git.
type cappedWriter struct {
	w    io.Writer
	max  int64
	n    int64
	over bool
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	if c.n+int64(len(p)) > c.max {
		c.over = true
		return 0, ErrTooLarge
	}
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

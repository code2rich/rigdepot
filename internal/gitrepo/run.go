// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// repoVariables point git at a repository other than the one named on its command line. They
// are set when rgd is started from inside a git hook or an editor's terminal, and must not leak
// into our commands.
var repoVariables = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_COMMON_DIR", "GIT_PREFIX",
	"GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM", "GIT_QUARANTINE_PATH",
}

// noPrompt are the variables that keep git, ssh and credential managers from asking anything.
// GIT_ASKPASS set to the empty string stops git from looking further for an askpass program
// (core.askPass, SSH_ASKPASS); an unset one would not.
var noPrompt = map[string]string{
	"GIT_TERMINAL_PROMPT": "0",
	"GIT_ASKPASS":         "",
	"SSH_ASKPASS":         "",
	"SSH_ASKPASS_REQUIRE": "never",
	"GCM_INTERACTIVE":     "never",
}

// environment returns the environment of a git command: the person's, so that their proxy and
// SSH agent apply, minus the repository-location variables, plus the variables that forbid
// prompts and the allowed transports.
func environment(allow transport) []string {
	drop := map[string]bool{"GIT_ALLOW_PROTOCOL": true}
	for _, name := range repoVariables {
		drop[name] = true
	}
	for name := range noPrompt {
		drop[name] = true
	}
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !drop[strings.ToUpper(name)] {
			env = append(env, kv)
		}
	}
	for name, value := range noPrompt {
		env = append(env, name+"="+value)
	}
	if allow != "" {
		env = append(env, "GIT_ALLOW_PROTOCOL="+string(allow))
	}
	return env
}

// baseConfig is passed with -c to every command. Hooks point at the null device, so that none
// can run; core.autocrlf and core.eol are pinned so that archive returns the stored bytes
// whatever the person's own settings say (git archive applies them like a checkout would).
func baseConfig() []string {
	return []string{
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.autocrlf=false",
		"-c", "core.eol=lf",
		"-c", "core.fsmonitor=false",
		"-c", "credential.interactive=false",
	}
}

// command describes one git invocation.
type command struct {
	gitDir string    // --git-dir, or "" for commands that need no repository
	allow  transport // transport git may use; "" for commands that do not reach a remote
	args   []string
	stdout io.Writer // nil: collect and return it
	shown  *address  // the address in args, to be replaced by its redacted form in messages
}

// run runs git with a timeout and returns what it printed on standard output. A failure carries
// the end of standard error.
func (f *Fetcher) run(ctx context.Context, c command) ([]byte, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, f.timeout, errTimedOut)
	defer cancel()

	args := baseConfig()
	if c.gitDir != "" {
		args = append(args, "--git-dir="+c.gitDir)
	}
	args = append(args, c.args...)
	cmd := exec.CommandContext(ctx, f.git, args...)
	cmd.Env = environment(c.allow)
	cmd.Stdin = nil // the null device: nothing to answer a prompt with
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	if c.stdout != nil {
		cmd.Stdout = c.stdout
	}
	cmd.Stderr = &limitedBuffer{max: 8 << 10, buf: &stderr}
	detach(cmd)
	cmd.WaitDelay = 5 * time.Second

	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	if context.Cause(ctx) == errTimedOut {
		return nil, fmt.Errorf("%w: git %s did not finish within %s", ErrTimeout, c.args[0], f.timeout)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return nil, f.notFound()
	}
	msg := strings.TrimSpace(stderr.String())
	if c.shown != nil && c.shown.arg != c.shown.shown {
		msg = strings.ReplaceAll(msg, c.shown.arg, c.shown.shown)
	}
	if msg == "" {
		msg = err.Error()
	}
	return nil, &gitError{op: c.args[0], msg: msg, err: err}
}

var errTimedOut = errors.New("timed out")

// gitError is a git command that failed.
type gitError struct {
	op  string
	msg string
	err error
}

func (e *gitError) Error() string { return "git " + e.op + ": " + e.msg }
func (e *gitError) Unwrap() error { return e.err }

// limitedBuffer keeps the last max bytes written to it, so that a chatty failure does not fill
// memory but its end, where git puts the reason, is kept.
type limitedBuffer struct {
	max int
	buf *bytes.Buffer
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.buf.Write(p)
	if over := b.buf.Len() - b.max; over > 0 {
		b.buf.Next(over)
	}
	return n, nil
}

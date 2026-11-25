// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package doctor

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/keyring"
)

// Checks returns the checks of this version, in the order people read them: the local data
// directory and what is in it first, then the programs it depends on, then the AI tools.
func Checks() []Check {
	return []Check{
		homeCheck{},
		configCheck{},
		manifestCheck{},
		intentsCheck{},
		leftoversCheck{},
		gitCheck{},
		keyringCheck{},
		toolsCheck{},
		toolRootsCheck{},
		// The checks that need a server (logged in, server reachable, versions compatible)
		// come with the server, in M1, after these.
	}
}

// Check is one self-check item.
type Check interface {
	// ID is the stable identifier of the check, such as "manifest".
	ID() string
	// Title is the message key of the check's name for people, such as msg.DoctorManifestTitle.
	Title() string
	// Run looks and reports. It returns at least one Result and changes nothing it looks at.
	Run(ctx context.Context, env *Env) []Result
}

// Status is the verdict of one result. The zero value is Unknown, so that a result nobody filled
// in is never taken for a pass.
type Status int

const (
	// Unknown: the check could not find out, because something could not be read.
	Unknown Status = iota
	// OK: nothing to do.
	OK
	// Skipped: the check does not apply here, such as a tool that is not installed.
	Skipped
	// Warn: works, but the person should look at it.
	Warn
	// Fail: something is broken and stops the program from doing its work.
	Fail
)

// String returns the name used in the JSON output: "unknown", "ok", "skipped", "warn" or "fail".
func (s Status) String() string {
	switch s {
	case OK:
		return "ok"
	case Skipped:
		return "skipped"
	case Warn:
		return "warn"
	case Fail:
		return "fail"
	}
	return "unknown"
}

// MarshalText makes a Status appear by name in JSON.
func (s Status) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// severity orders statuses by how much attention they need.
func (s Status) severity() int {
	switch s {
	case OK, Skipped:
		return 0
	case Warn:
		return 1
	case Unknown:
		return 2
	}
	return 3
}

// Message is a text for people as a message key and its placeholder values. Rendering it is up
// to the caller, in the person's language.
type Message struct {
	Key  string
	Args []i18n.Arg
}

// M returns a message.
func M(key string, args ...i18n.Arg) Message { return Message{Key: key, Args: args} }

// IsZero reports whether there is no message.
func (m Message) IsZero() bool { return m.Key == "" }

// Text renders the message with p.
func (m Message) Text(p *i18n.Printer) string {
	if m.IsZero() {
		return ""
	}
	return p.T(m.Key, m.Args...)
}

// Result is one finding of a check.
type Result struct {
	// Check is the ID of the check and Title the message key of its name. Run fills them in.
	Check string
	Title string
	// Subject is what the result is about when a check reports on several things, such as a
	// tool's ID; "" when it is about the check as a whole.
	Subject string
	Status  Status
	// Code is a stable identifier of what was found, such as "too_open". Scripts match on it;
	// it is never translated.
	Code string
	// Message says what was found.
	Message Message
	// Hint says what to do about it; zero when there is nothing to do.
	Hint Message
	// Details are further lines, such as one per unfinished write.
	Details []Message
	// Path is the file or directory the result is about, absolute, or "".
	Path string
	// Facts are machine-readable values for the JSON output, such as a version.
	Facts map[string]string
}

// Report is what a run of the checks found, in order.
type Report struct {
	Results []Result
}

// Worst returns the status that needs the most attention; OK when there are no results.
func (r Report) Worst() Status {
	worst := OK
	for _, res := range r.Results {
		if res.Status.severity() > worst.severity() {
			worst = res.Status
		}
	}
	return worst
}

// Count returns how many results have the given status.
func (r Report) Count(s Status) int {
	n := 0
	for _, res := range r.Results {
		if res.Status == s {
			n++
		}
	}
	return n
}

// Run runs the checks one after the other and collects their results.
func Run(ctx context.Context, env *Env, checks []Check) Report {
	var r Report
	for _, c := range checks {
		for _, res := range c.Run(ctx, env) {
			res.Check, res.Title = c.ID(), c.Title()
			r.Results = append(r.Results, res)
		}
	}
	return r
}

// Env is everything the checks look at. The command fills it from the real computer; tests
// describe one of their own.
type Env struct {
	// Home is the local data directory, which may not exist yet. HomeErr is set when where it
	// is cannot be known; Home is then the zero value.
	Home    home.Dir
	HomeErr error

	// UserHome is the person's home directory, absolute, or "" when it cannot be known.
	UserHome string
	// GOOS is the operating system, as in runtime.GOOS.
	GOOS string
	// Getenv returns an environment variable.
	Getenv func(string) string

	// Git is the git program the configuration names, or "" for git on PATH.
	Git string
	// Server is the configured server address, or "" when there is none.
	Server string
	// DisabledTools are the tool IDs the local configuration switches off.
	DisabledTools []string

	// Keyring looks at where the device key and token are kept. Nil means the real store of
	// Home (package keyring), which inside tests never reaches the system keychain.
	Keyring func() keyring.Status

	// Adapters are the tool adapters this program contains, and AdapterEnv is what they see of
	// the computer.
	Adapters   []adapter.Adapter
	AdapterEnv adapter.Env

	// ShowPath shortens a path for people (~/… on macOS and Linux). Nil shows paths unchanged.
	ShowPath func(string) string
	// Now returns the current time. Nil means time.Now.
	Now func() time.Time
	// LockWait is how long to wait for another program to let go of the lock before reporting
	// that it is busy. Zero means DefaultLockWait.
	LockWait time.Duration

	detectOnce sync.Once
	detected   []Detected
}

// DefaultLockWait is how long a check waits for the lock by default. Another program that holds
// it is in the middle of a change, so waiting long would not help.
const DefaultLockWait = 2 * time.Second

func (e *Env) show(p string) string {
	if e.ShowPath == nil {
		return p
	}
	return e.ShowPath(p)
}

func (e *Env) now() time.Time {
	if e.Now == nil {
		return time.Now()
	}
	return e.Now()
}

func (e *Env) getenv(k string) string {
	if e.Getenv == nil {
		return ""
	}
	return e.Getenv(k)
}

func (e *Env) lockWait() time.Duration {
	if e.LockWait <= 0 {
		return DefaultLockWait
	}
	return e.LockWait
}

// homeKnown reports whether the local data directory is known.
func (e *Env) homeKnown() bool {
	return e.HomeErr == nil && e.Home.Path() != ""
}

// Detected is what detecting one tool gave.
type Detected struct {
	Adapter   adapter.Adapter
	Detection adapter.Detection
	// Err is the error Detect returned; the tool's status is then Unknown.
	Err error
	// Disabled is true when the local configuration switches the tool off; it was not detected.
	Disabled bool
}

// Detections detects every tool once and returns the same answer to every check that asks.
func (e *Env) Detections(ctx context.Context) []Detected {
	e.detectOnce.Do(func() {
		for _, a := range e.Adapters {
			d := Detected{Adapter: a}
			if slices.Contains(e.DisabledTools, a.ID()) {
				d.Disabled = true
			} else {
				d.Detection, d.Err = a.Detect(ctx, e.AdapterEnv)
				if d.Err != nil {
					d.Detection.Status = adapter.Unknown
				}
			}
			e.detected = append(e.detected, d)
		}
	})
	return e.detected
}

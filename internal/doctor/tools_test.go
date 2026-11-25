// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package doctor

import (
	"context"
	"errors"
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter"
)

// fakeAdapter is an adapter whose detection is fixed. The registry of the real program may be
// empty while the real adapters are being written; the checks are tested with this one.
type fakeAdapter struct {
	id        string
	verified  string // "" for a profile without a verification record
	detection adapter.Detection
	err       error
	calls     int
	env       adapter.Env // the Env Detect was given
}

func (a *fakeAdapter) ID() string { return a.id }

func (a *fakeAdapter) Profile() adapter.Profile {
	p := adapter.Profile{ID: a.id, Name: "Fake " + a.id}
	if a.verified != "" {
		p.Verified = &adapter.Verification{Version: a.verified, OS: []string{"darwin"}, At: "2026-11-24", How: "a test"}
	}
	return p
}

func (a *fakeAdapter) Detect(_ context.Context, env adapter.Env) (adapter.Detection, error) {
	a.calls++
	a.env = env
	return a.detection, a.err
}

func (a *fakeAdapter) Capabilities(adapter.Detection) adapter.Capabilities {
	return adapter.Capabilities{}
}

func (a *fakeAdapter) Plan(context.Context, adapter.PlanRequest) (adapter.Plan, error) {
	return adapter.Plan{}, nil
}

func installed(version string) adapter.Detection {
	return adapter.Detection{Status: adapter.Installed, Version: version, Roots: map[string]string{"config": "~/.fake"}}
}

func TestToolsDetected(t *testing.T) {
	env := newTestEnv(t)
	env.Adapters = []adapter.Adapter{
		&fakeAdapter{id: "fake-ok", verified: "2.1.330", detection: installed("2.1.400")},
		&fakeAdapter{id: "fake-absent", verified: "1.0", detection: adapter.Detection{Status: adapter.NotInstalled}},
		&fakeAdapter{id: "fake-off", verified: "1.0", detection: installed("1.0")},
	}
	env.DisabledTools = []string{"fake-off"}
	rs := run(t, toolsCheck{}, env)
	if len(rs) != 3 {
		t.Fatalf("one result per tool: %+v", rs)
	}
	expect(t, rs[0], OK, "installed")
	expect(t, rs[1], Skipped, "not_installed")
	expect(t, rs[2], Skipped, "disabled")
	if rs[0].Subject != "fake-ok" || rs[0].Facts["version"] != "2.1.400" || rs[0].Facts["status"] != "installed" {
		t.Errorf("result: %+v", rs[0])
	}
	if rs[2].Facts["status"] != "disabled" || env.Adapters[2].(*fakeAdapter).calls != 0 {
		t.Errorf("a switched-off tool is not detected: %+v", rs[2])
	}
}

// TestToolsDetectWithTheirOwnEnv: each adapter detects with the Env made for its profile, the
// way the sync engine gives it (sync.System.AdapterEnv).
func TestToolsDetectWithTheirOwnEnv(t *testing.T) {
	env := newTestEnv(t)
	a, b := &fakeAdapter{id: "fake-a", verified: "1.0", detection: installed("1.0")}, &fakeAdapter{id: "fake-b", verified: "1.0", detection: installed("1.0")}
	env.Adapters = []adapter.Adapter{a, b}
	env.AdapterEnv = func(p adapter.Profile) adapter.Env { return adapter.Env{Home: "/home/" + p.ID} }
	run(t, toolsCheck{}, env)
	if a.env.Home != "/home/fake-a" || b.env.Home != "/home/fake-b" {
		t.Errorf("detected with %q and %q", a.env.Home, b.env.Home)
	}
}

func TestToolsProblems(t *testing.T) {
	env := newTestEnv(t)
	env.Adapters = []adapter.Adapter{
		&fakeAdapter{id: "fake-unknown", verified: "1.0", detection: adapter.Detection{Status: adapter.Unknown, Reason: adapter.ReasonUnreadable}},
		&fakeAdapter{id: "fake-error", verified: "1.0", err: errors.New("context canceled")},
		&fakeAdapter{id: "fake-unverified", detection: installed("1.0")},
		&fakeAdapter{id: "fake-newer", verified: "0.167.0", detection: installed("1.2.0")},
	}
	rs := run(t, toolsCheck{}, env)
	if len(rs) != 4 {
		t.Fatalf("one result per tool: %+v", rs)
	}
	expect(t, rs[0], Unknown, "unknown")
	if len(rs[0].Details) != 1 || rs[0].Details[0].Key != adapter.ReasonUnreadable {
		t.Errorf("the adapter's reason is not shown: %+v", rs[0].Details)
	}
	expect(t, rs[1], Unknown, "detect_failed")
	expect(t, rs[2], Warn, "unverified")
	expect(t, rs[3], Warn, "newer_than_verified")
}

func TestToolsNoneSupported(t *testing.T) {
	expect(t, only(t, toolsCheck{}, newTestEnv(t)), Warn, "no_adapters")
}

func TestNewerMajor(t *testing.T) {
	for _, tt := range []struct {
		version, verified string
		want              bool
	}{
		{"2.1.400", "2.1.330", false},
		{"3.0.0", "2.1.330", true},
		{"v1.0", "0.167.0", true},
		{"0.170.1", "0.167.0", false},
		{"", "1.0", false},
		{"nightly", "1.0", false},
	} {
		if got := newerMajor(tt.version, tt.verified); got != tt.want {
			t.Errorf("newerMajor(%q, %q) = %v", tt.version, tt.verified, got)
		}
	}
}

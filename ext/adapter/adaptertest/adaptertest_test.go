// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package adaptertest

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/adapter/profileyaml"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/testenv"
)

// The suite's own tests are isolated like every adapter's; Main is testenv.Main.
func TestMain(m *testing.M) { testenv.Main(m) }

// fakeAdapter is Fake Code: an adapter made from nothing but its profile, the way a tool shaped
// like the others is supported.
func fakeAdapter(t *testing.T) adapter.Adapter {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "profile.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := profileyaml.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return adapter.FromProfile(p)
}

// The well-behaved fake passes the whole contract. This is what an adapter's TestContract looks
// like.
func TestContract(t *testing.T) {
	Run(t, fakeAdapter(t), "testdata")
}

// broken wraps the fake and changes one thing about its plans.
type broken struct {
	adapter.Adapter
	plan func(ctx context.Context, req adapter.PlanRequest, p adapter.Plan) adapter.Plan
}

func (b broken) Plan(ctx context.Context, req adapter.PlanRequest) (adapter.Plan, error) {
	p, err := b.Adapter.Plan(ctx, req)
	if err != nil {
		return p, err
	}
	return b.plan(ctx, req, p), nil
}

// mapOps changes every operation of a plan.
func mapOps(f func(adapter.Op) adapter.Op) func(context.Context, adapter.PlanRequest, adapter.Plan) adapter.Plan {
	return func(_ context.Context, _ adapter.PlanRequest, p adapter.Plan) adapter.Plan {
		ops := make([]adapter.Op, len(p.Ops))
		for i, op := range p.Ops {
			ops[i] = f(op)
		}
		p.Ops = ops
		return p
	}
}

// record runs the suite against an adapter and returns what failed, without failing this test.
func record(t *testing.T, a adapter.Adapter) recorder {
	t.Helper()
	rec := newRecorder(t.TempDir())
	// Never -update here: a broken adapter must not become the expected output.
	rec.run("suite", func(r reporter) { runSuite(r, a, "testdata", false) })
	return rec
}

func describeFailures(rec recorder) string {
	var lines []string
	for _, f := range *rec.failures {
		lines = append(lines, "  "+f.test+" ["+string(f.check)+"] "+firstLine(f.msg))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// The recorder sees the same run as TestContract: nothing fails.
func TestRecorderSeesTheGoodAdapterPass(t *testing.T) {
	if rec := record(t, fakeAdapter(t)); len(*rec.failures) > 0 {
		t.Errorf("the well-behaved fake failed:\n%s", describeFailures(rec))
	}
}

// Each of these breaks the fake in one way. The assertion that guards against that way has to
// fail; if it does not, the suite would let such an adapter through.
func TestBrokenAdaptersFailTheirAssertion(t *testing.T) {
	cases := []struct {
		name    string
		breakIt func(adapter.Adapter) adapter.Adapter
		want    check
	}{
		{
			name: "writes a skill without the prefix",
			breakIt: func(a adapter.Adapter) adapter.Adapter {
				return broken{a, mapOps(func(op adapter.Op) adapter.Op {
					if o, ok := op.(adapter.WriteTree); ok {
						o.Dir = path.Join(path.Dir(o.Dir), strings.TrimPrefix(path.Base(o.Dir), brand.Prefix))
						return o
					}
					return op
				})}
			},
			want: checkTargets,
		},
		{
			name: "sets a key without the prefix",
			breakIt: func(a adapter.Adapter) adapter.Adapter {
				return broken{a, mapOps(func(op adapter.Op) adapter.Op {
					if o, ok := op.(adapter.SetKey); ok {
						o.Path = append(append([]string{}, o.Path[:len(o.Path)-1]...), "contract")
						return o
					}
					return op
				})}
			},
			want: checkTargets,
		},
		{
			name: "writes outside its declared places",
			breakIt: func(a adapter.Adapter) adapter.Adapter {
				return broken{a, mapOps(func(op adapter.Op) adapter.Op {
					if o, ok := op.(adapter.WriteTree); ok {
						o.Dir = "~/Documents/" + path.Base(o.Dir)
						return o
					}
					return op
				})}
			},
			want: checkTargets,
		},
		{
			name: "climbs out of its root with ..",
			breakIt: func(a adapter.Adapter) adapter.Adapter {
				return broken{a, mapOps(func(op adapter.Op) adapter.Op {
					if o, ok := op.(adapter.WriteFile); ok {
						o.Path = path.Dir(o.Path) + "/../../" + path.Base(o.Path)
						return o
					}
					return op
				})}
			},
			want: checkTargets,
		},
		{
			name: "plans a key in a file that does not parse",
			breakIt: func(a adapter.Adapter) adapter.Adapter {
				return broken{a, func(_ context.Context, req adapter.PlanRequest, p adapter.Plan) adapter.Plan {
					// Treat the unreadable file as empty: drop the skips and plan the servers.
					var skips []adapter.Skip
					for _, s := range p.Skips {
						if s.Reason != adapter.ConfigUnreadable {
							skips = append(skips, s)
							continue
						}
						p.Ops = append(p.Ops, adapter.SetKey{
							Resource: s.Resource, File: "~/.fakecode/settings.jsonc", Format: adapter.JSONC,
							Path: []string{"mcp", "servers", brand.Prefix + s.Resource.Code}, Value: map[string]any{"type": "stdio"},
						})
					}
					p.Skips = skips
					return p
				}}
			},
			want: checkUnreadable,
		},
		{
			name: "reports the unreadable file and still plans a key in it",
			breakIt: func(a adapter.Adapter) adapter.Adapter {
				return broken{a, func(_ context.Context, _ adapter.PlanRequest, p adapter.Plan) adapter.Plan {
					for _, s := range p.Skips {
						if s.Reason == adapter.ConfigUnreadable {
							p.Ops = append(p.Ops, adapter.SetKey{
								Resource: s.Resource, File: "~/.fakecode/settings.jsonc", Format: adapter.JSONC,
								Path: []string{"mcp", "servers", brand.Prefix + s.Resource.Code}, Value: map[string]any{"type": "stdio"},
							})
						}
					}
					return p
				}}
			},
			want: checkUnreadable,
		},
		{
			name: "writes a file while planning",
			breakIt: func(a adapter.Adapter) adapter.Adapter {
				return broken{a, func(_ context.Context, req adapter.PlanRequest, p adapter.Plan) adapter.Plan {
					_ = os.WriteFile(filepath.Join(req.Env.Home, "plan-was-here"), []byte("x"), 0o644)
					return p
				}}
			},
			want: checkPlanPure,
		},
		{
			name: "writes into the process's home directory while planning",
			breakIt: func(a adapter.Adapter) adapter.Adapter {
				return broken{a, func(_ context.Context, _ adapter.PlanRequest, p adapter.Plan) adapter.Plan {
					home, _ := os.UserHomeDir()
					f, _ := os.CreateTemp(home, "plan-")
					if f != nil {
						f.Close()
					}
					return p
				}}
			},
			want: checkPlanPure,
		},
		{
			name: "runs a command while planning",
			breakIt: func(a adapter.Adapter) adapter.Adapter {
				return broken{a, func(ctx context.Context, req adapter.PlanRequest, p adapter.Plan) adapter.Plan {
					_, _ = req.Env.Version(ctx, "fakecode", "mcp", "add", "x")
					return p
				}}
			},
			want: checkPlanPure,
		},
		{
			name: "plans differently every time",
			breakIt: func(a adapter.Adapter) adapter.Adapter {
				n := 0
				return broken{a, mapOps(func(op adapter.Op) adapter.Op {
					if o, ok := op.(adapter.WriteFile); ok {
						n++
						o.Data = append(append([]byte{}, o.Data...), []byte(strings.Repeat("\n", n))...)
						return o
					}
					return op
				})}
			},
			want: checkDeterministic,
		},
		{
			name: "puts the home directory it saw into the configuration",
			breakIt: func(a adapter.Adapter) adapter.Adapter {
				return broken{a, func(ctx context.Context, req adapter.PlanRequest, p adapter.Plan) adapter.Plan {
					return mapOps(func(op adapter.Op) adapter.Op {
						if o, ok := op.(adapter.SetKey); ok {
							v := map[string]any{}
							for k, x := range o.Value.(map[string]any) {
								v[k] = x
							}
							v["cwd"] = req.Env.Home // differs between the preview copy and the real home
							o.Value = v
							return o
						}
						return op
					})(ctx, req, p)
				}}
			},
			want: checkPreview,
		},
		{
			name: "writes the MCP entry in another shape",
			breakIt: func(a adapter.Adapter) adapter.Adapter {
				return broken{a, mapOps(func(op adapter.Op) adapter.Op {
					if o, ok := op.(adapter.SetKey); ok {
						v := map[string]any{}
						for k, x := range o.Value.(map[string]any) {
							if k != "type" {
								v[k] = x
							}
						}
						o.Value = v
						return o
					}
					return op
				})}
			},
			want: checkGolden,
		},
		{
			name: "drops an artifact without saying so",
			breakIt: func(a adapter.Adapter) adapter.Adapter {
				return broken{a, func(_ context.Context, _ adapter.PlanRequest, p adapter.Plan) adapter.Plan {
					p.Skips = nil
					return p
				}}
			},
			want: checkGolden,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := record(t, c.breakIt(fakeAdapter(t)))
			t.Logf("what failed (go test -v shows this):\n%s", describeFailures(rec))
			if rec.checks()[c.want] == 0 {
				t.Errorf("the suite let this adapter through: [%s] did not fail. What failed:\n%s", c.want, describeFailures(rec))
			}
		})
	}
}

// The assertions that guard the core (drift, uninstall, occupied, others) cannot be broken by an
// adapter: they hold because of what the executor and the sync engine do. Breaking the core
// stand-in shows that they would catch a core that got it wrong.
func TestBrokenCoreFailsItsAssertions(t *testing.T) {
	cases := []struct {
		name string
		ex   func(home, state string, places []string) executor
		want []check
	}{
		{
			name: "undo leaves the directories it created",
			ex: func(home, state string, places []string) executor {
				return keepDirs{newStandInExecutor(home, state, places)}
			},
			want: []check{checkUninstall},
		},
		{
			name: "overwrites what it finds",
			ex: func(home, state string, places []string) executor {
				return &trusting{standInExecutor: newStandInExecutor(home, state, places), seen: map[string]string{}}
			},
			want: []check{checkDrift, checkOccupied},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			saved := newExecutor
			newExecutor = c.ex
			defer func() { newExecutor = saved }()
			rec := record(t, fakeAdapter(t))
			for _, want := range c.want {
				if rec.checks()[want] == 0 {
					t.Errorf("[%s] did not fail. What failed:\n%s", want, describeFailures(rec))
				}
			}
		})
	}
}

// keepDirs undoes entries but leaves the directories it created.
type keepDirs struct{ *standInExecutor }

func (k keepDirs) Revert(e entry) (bool, error) {
	u, _ := e.undo.(standInUndo)
	u.createdDirs = nil
	e.undo = u
	return k.standInExecutor.Revert(e)
}

// trusting does not look: it takes every target to be as it last left it, and anything it did
// not write to be absent. A core like that overwrites the person's things and their changes.
type trusting struct {
	*standInExecutor
	seen map[string]string
}

func (x *trusting) Observe(t target) observed {
	if d, ok := x.seen[t.id()]; ok {
		return observed{state: present, digest: d}
	}
	return observed{state: absent}
}

func (x *trusting) Apply(op adapter.Op, _ observed) (entry, error) {
	e, err := x.standInExecutor.Apply(op, x.standInExecutor.Observe(targetOf(op)))
	if err == nil {
		x.seen[e.target.id()] = e.digest
	}
	return e, err
}

func (x *trusting) Revert(e entry) (bool, error) {
	delete(x.seen, e.target.id())
	return x.standInExecutor.Revert(e)
}

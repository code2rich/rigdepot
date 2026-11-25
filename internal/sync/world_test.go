// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package sync

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	stdsync "sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/desired"
	"github.com/code2rich/rigdepot/internal/executor"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/manifest"
	"github.com/code2rich/rigdepot/internal/repoformat"
	"github.com/code2rich/rigdepot/internal/sandbox"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

var (
	ctx      = context.Background()
	fixedNow = func() time.Time { return time.Date(2026, 11, 25, 9, 30, 0, 0, time.UTC) }
	verified = &adapter.Verification{Version: "1.0.0", OS: []string{"darwin", "linux", "windows"}, At: "2026-11-25", How: "a tool that exists only in these tests"}
)

// The real adapters and kinds are in builtin/, which the core must not import. These are built
// the way a tool shaped like the others is: from nothing but a profile.

// fakeCode takes skills, slash commands and MCP servers; its root is ~/.fakecode.
func fakeCode() adapter.Adapter {
	return adapter.FromProfile(adapter.Profile{
		ID: "fakecode", Name: "Fake Code",
		Detect:      adapter.Detect{Dirs: []string{"{config}"}},
		Roots:       map[string]adapter.Root{"config": {Env: "FAKECODE_HOME", Default: "~/.fakecode"}},
		Verified:    verified,
		Skills:      &adapter.SkillsTarget{Dir: "{config}/skills", Layout: adapter.LayoutDir},
		Commands:    &adapter.FilesTarget{Dir: "{config}/commands", Ext: ".md"},
		MCP:         &adapter.MCPTarget{Files: []string{"{config}/settings.jsonc"}, Format: adapter.JSONC, Path: []string{"mcp", "servers"}},
		Credentials: []string{"auth.json"},
	})
}

// otherAI takes skills only, and reads Fake Code's skill directory as well.
func otherAI() adapter.Adapter {
	return adapter.FromProfile(adapter.Profile{
		ID: "otherai", Name: "Other AI",
		Detect:    adapter.Detect{Dirs: []string{"{config}"}},
		Roots:     map[string]adapter.Root{"config": {Default: "~/.otherai"}},
		Verified:  verified,
		Skills:    &adapter.SkillsTarget{Dir: "{config}/skills", Layout: adapter.LayoutDir},
		AlsoReads: []adapter.SharedDir{{Kind: "skill", Dir: "~/.fakecode/skills"}},
	})
}

// spec is what the fake kinds parse a resource into.
type spec struct {
	ref   resource.Ref
	files fs.FS
}

func treeFiles(files fs.FS) ([]adapter.File, error) {
	var out []adapter.File
	err := fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || p == repoformat.MetaFile {
			return err
		}
		data, err := fs.ReadFile(files, p)
		out = append(out, adapter.File{Path: p, Data: data})
		return err
	})
	return out, err
}

// skillKind is skills/<code>/: delivered as a skill directory, or not at all.
type skillKind struct{}

func (skillKind) ID() string          { return resource.KindSkill }
func (skillKind) Layout() kind.Layout { return kind.Layout{Dir: "skills"} }
func (skillKind) Schema() []byte      { return nil }
func (skillKind) Parse(ref resource.Ref, files fs.FS) (kind.Spec, error) {
	if _, err := fs.Stat(files, "SKILL.md"); err != nil {
		return nil, errors.New("a skill needs a SKILL.md")
	}
	return spec{ref: ref, files: files}, nil
}
func (skillKind) Forms() []kind.Form {
	return []kind.Form{{Needs: kind.Needs{Skills: true}, Render: asSkill}}
}

func asSkill(s kind.Spec, _ adapter.Capabilities, opt kind.RenderOptions) ([]adapter.Artifact, error) {
	sp := s.(spec)
	files, err := treeFiles(sp.files)
	if err != nil {
		return nil, err
	}
	return []adapter.Artifact{adapter.SkillDir{Resource: sp.ref, DirName: opt.Prefix + sp.ref.Code, Files: files}}, nil
}

// flowKind is flows/<code>/ with a FLOW.md: a slash command, or a skill where there are none.
type flowKind struct{}

func (flowKind) ID() string          { return resource.KindWorkflow }
func (flowKind) Layout() kind.Layout { return kind.Layout{Dir: "flows"} }
func (flowKind) Schema() []byte      { return nil }
func (flowKind) Parse(ref resource.Ref, files fs.FS) (kind.Spec, error) {
	if _, err := fs.Stat(files, "FLOW.md"); err != nil {
		return nil, errors.New("a flow needs a FLOW.md")
	}
	return spec{ref: ref, files: files}, nil
}
func (flowKind) Forms() []kind.Form {
	return []kind.Form{
		{Needs: kind.Needs{Commands: true}, Render: func(s kind.Spec, _ adapter.Capabilities, opt kind.RenderOptions) ([]adapter.Artifact, error) {
			sp := s.(spec)
			body, err := fs.ReadFile(sp.files, "FLOW.md")
			return []adapter.Artifact{adapter.Command{Resource: sp.ref, CmdName: opt.Prefix + sp.ref.Code, Description: "a flow", Body: string(body)}}, err
		}},
		{Needs: kind.Needs{Skills: true}, Degraded: "workflow.as_skill", Render: func(s kind.Spec, _ adapter.Capabilities, opt kind.RenderOptions) ([]adapter.Artifact, error) {
			sp := s.(spec)
			body, err := fs.ReadFile(sp.files, "FLOW.md")
			files := []adapter.File{{Path: "SKILL.md", Data: append([]byte("---\nname: "+opt.Prefix+sp.ref.Code+"\ndescription: a flow\n---\n"), body...)}}
			return []adapter.Artifact{adapter.SkillDir{Resource: sp.ref, DirName: opt.Prefix + sp.ref.Code, Files: files, Degraded: "workflow.as_skill"}}, err
		}},
	}
}

// serverKind is servers/<code>/ with a server.txt holding a command: an MCP server.
type serverKind struct{}

func (serverKind) ID() string          { return resource.KindMCP }
func (serverKind) Layout() kind.Layout { return kind.Layout{Dir: "servers"} }
func (serverKind) Schema() []byte      { return nil }
func (serverKind) Parse(ref resource.Ref, files fs.FS) (kind.Spec, error) {
	cmd, err := fs.ReadFile(files, "server.txt")
	if err != nil {
		return nil, err
	}
	return adapter.MCPServer{Resource: ref, EntryName: brand.Prefix + ref.Code, Transport: adapter.Stdio, Command: strings.TrimSpace(string(cmd))}, nil
}
func (serverKind) Forms() []kind.Form {
	return []kind.Form{{Needs: kind.Needs{MCP: true}, Render: func(s kind.Spec, _ adapter.Capabilities, _ kind.RenderOptions) ([]adapter.Artifact, error) {
		return []adapter.Artifact{s.(adapter.MCPServer)}, nil
	}}}
}

func kinds() *kind.Registry {
	r := kind.NewRegistry()
	r.Register(skillKind{})
	r.Register(flowKind{})
	r.Register(serverKind{})
	return r
}

// repo is a resource repository, file by file.
type repo map[string]string

func (r repo) skill(code, body string) repo {
	r["skills/"+code+"/SKILL.md"] = "---\nname: " + code + "\ndescription: " + code + "\n---\n" + body
	r["skills/"+code+"/meta.yaml"] = "name: " + code + "\n"
	return r
}

func (r repo) without(code string) repo {
	for p := range r {
		if strings.Contains(p, "/"+code+"/") {
			delete(r, p)
		}
	}
	return r
}

func newRepo() repo { return repo{brand.RepoConfig: "schema: 1\n"} }

func (r repo) state(t *testing.T, f desired.Filter) *desired.State {
	t.Helper()
	fsys := fstest.MapFS{}
	for p, s := range r {
		fsys[p] = &fstest.MapFile{Data: []byte(s)}
	}
	read, err := repoformat.Read(fsys, repoformat.Options{Kinds: kinds()})
	if err != nil {
		t.Fatal(err)
	}
	return desired.FromRepo(read, desired.Source{Type: desired.SourceRepo, URL: "https://git.example.com/resources.git"}, f)
}

// world is a person's home directory with the tools in it, and an engine for it.
type world struct {
	t     *testing.T
	home  string
	data  home.Dir
	eng   *Engine
	log   *opLog
	keys  *keyStore
	env   map[string]string
	tools []adapter.Adapter
}

func newWorld(t *testing.T, tools ...adapter.Adapter) *world {
	t.Helper()
	if len(tools) == 0 {
		tools = []adapter.Adapter{fakeCode()}
	}
	h := t.TempDir()
	data, err := home.At(filepath.Join(h, brand.Home))
	if err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, home: h, data: data, log: &opLog{}, keys: &keyStore{byHome: map[string]map[string]string{}}, env: map[string]string{}, tools: tools}
	for _, a := range tools {
		w.mkdir("~/." + a.ID()) // the tool has been used here
	}
	// What the person has of their own: a skill with the prefix that is not in any manifest.
	w.write("~/.fakecode/skills/"+brand.Prefix+"mine/SKILL.md", "my own skill\n")
	w.write("~/.fakecode/settings.jsonc", "{\n  // mine\n  \"theme\": \"dark\",\n}\n")
	w.eng = w.engine()
	return w
}

func (w *world) engine() *Engine {
	w.t.Helper()
	e, err := New(Config{
		Adapters: w.tools,
		Data:     w.data,
		System: System{
			Home:     w.home,
			Getenv:   func(k string) string { return w.env[k] },
			LookPath: func(name string) (string, error) { return "", &exec.Error{Name: name, Err: exec.ErrNotFound} },
			Version:  func(context.Context, string, ...string) (string, error) { return "1.0.0", nil },
		},
		NewExecutor: func(r executor.Roots, st executor.State) (Executor, error) {
			real, err := executor.New(r, st)
			if err != nil {
				return nil, err
			}
			name := "preview"
			if r.Home == w.home {
				name = "real"
			}
			return &watched{Executor: real, name: name, log: w.log, keys: w.keys, home: r.Home, journal: st.Journal}, nil
		},
		Now:      fixedNow,
		LockWait: 2 * time.Second,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return e
}

func (w *world) path(p string) string {
	return filepath.Join(w.home, filepath.FromSlash(strings.TrimPrefix(p, "~/")))
}

func (w *world) write(p, content string) {
	w.t.Helper()
	full := w.path(p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) mkdir(p string) {
	w.t.Helper()
	if err := os.MkdirAll(w.path(p), 0o755); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) read(p string) string {
	w.t.Helper()
	data, err := os.ReadFile(w.path(p))
	if err != nil {
		w.t.Fatal(err)
	}
	return string(data)
}

func (w *world) exists(p string) bool {
	_, err := os.Lstat(w.path(p))
	return err == nil
}

// snapshot is the home directory, without the local data directory, as "~/path" → content
// ("<dir>" for a directory).
func (w *world) snapshot() map[string]string {
	w.t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(w.home, func(p string, d fs.DirEntry, err error) error {
		rel, _ := filepath.Rel(w.home, p)
		rel = filepath.ToSlash(rel)
		if err != nil && d != nil && d.IsDir() && errors.Is(err, fs.ErrPermission) {
			out["~/"+rel] = "<unreadable>"
			return filepath.SkipDir
		}
		if err != nil {
			return err
		}
		switch {
		case rel == ".":
			return nil
		case rel == brand.Home:
			return filepath.SkipDir
		case d.IsDir():
			out["~/"+rel] = "<dir>"
			return nil
		}
		data, err := os.ReadFile(p)
		out["~/"+rel] = string(data)
		return err
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

// changes describes the difference of two snapshots the way a preview reports it.
func changes(before, after map[string]string) []string {
	var out []string
	for p, a := range after {
		b, ok := before[p]
		switch {
		case !ok:
			out = append(out, "created "+p)
		case a != b:
			out = append(out, "modified "+p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			out = append(out, "removed "+p)
		}
	}
	slices.Sort(out)
	return out
}

func previewChanges(r sandbox.Result) []string {
	var out []string
	for _, c := range r.Changes {
		out = append(out, string(c.Action)+" "+c.Path)
	}
	slices.Sort(out)
	return out
}

func (w *world) manifest() *manifest.Manifest {
	w.t.Helper()
	m, err := manifest.Open(w.data).Read()
	if err != nil {
		w.t.Fatal(err)
	}
	return m
}

func (w *world) plan(st *desired.State, opt Options) *Plan {
	w.t.Helper()
	p, err := w.eng.Plan(ctx, st, opt)
	if err != nil {
		w.t.Fatal(err)
	}
	return p
}

// run is a whole sync: plan, preview, lock, apply. It checks on the way that the preview
// changed what the real write changed, and that both carried out the same operations.
func (w *world) run(st *desired.State, opt Options, in ConsentInput) (*Plan, *Result) {
	w.t.Helper()
	s, err := w.eng.Lock(ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	defer s.Release()
	p := w.plan(st, opt)
	w.log.reset()
	prev, err := w.eng.Preview(ctx, p)
	if err != nil {
		w.t.Fatalf("Preview: %v", err)
	}
	before := w.snapshot()
	ap := p.Approve(in, fixedNow())
	res, err := s.Apply(ctx, p, ap)
	if err != nil {
		w.t.Fatalf("Apply: %v", err)
	}
	if len(res.Deferred) == 0 && len(res.Retry) == 0 {
		if got, want := previewChanges(prev), changes(before, w.snapshot()); !slices.Equal(got, want) {
			w.t.Errorf("the preview and the write differ\npreview:\n  %s\nwritten:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
		if pv, rl := w.log.get("preview"), w.log.get("real"); !slices.Equal(pv, rl) {
			w.t.Errorf("the preview and the write carried out different operations\npreview:\n  %s\nwritten:\n  %s", strings.Join(pv, "\n  "), strings.Join(rl, "\n  "))
		}
	}
	return p, res
}

// item returns the plan's item for a tool and a path, failing the test without one.
func item(t *testing.T, p *Plan, tool, path string) Item {
	t.Helper()
	for _, it := range p.Items {
		if it.Tool == tool && it.Target.Path == path {
			return it
		}
	}
	var all []string
	for _, it := range p.Items {
		all = append(all, fmt.Sprintf("%s %s %s", it.Tool, it.Action, it.Target.Path))
	}
	t.Fatalf("no item for %s %s in the plan:\n  %s", tool, path, strings.Join(all, "\n  "))
	return Item{}
}

func toolOf(t *testing.T, p *Plan, id string) Tool {
	t.Helper()
	for _, tl := range p.Tools {
		if tl.ID == id {
			return tl
		}
	}
	t.Fatalf("no tool %s in the plan", id)
	return Tool{}
}

// opLog records the operations the executors carried out, by executor.
type opLog struct {
	mu  stdsync.Mutex
	ops map[string][]string
}

func (l *opLog) add(name, s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ops == nil {
		l.ops = map[string][]string{}
	}
	l.ops[name] = append(l.ops[name], s)
}

func (l *opLog) get(name string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.ops[name])
}

func (l *opLog) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ops = nil
}

// keyStore stands in for configuration keys until the executor can set them (M0-20): per home
// directory, the digest of each key.
type keyStore struct {
	mu     stdsync.Mutex
	byHome map[string]map[string]string
}

// watched is the real executor, with every operation recorded and configuration keys kept in
// a keyStore.
type watched struct {
	Executor
	name    string
	log     *opLog
	keys    *keyStore
	home    string
	journal executor.Journal
}

func keyID(t executor.Target) string { return t.Path + "#" + strings.Join(t.Key, ".") }

func (x *watched) Observe(t executor.Target) executor.Observed {
	if t.Op != manifest.OpKey {
		return x.Executor.Observe(t)
	}
	x.keys.mu.Lock()
	defer x.keys.mu.Unlock()
	if d, ok := x.keys.byHome[x.home][keyID(t)]; ok {
		return executor.Observed{State: executor.Present, Digest: d}
	}
	return executor.Observed{State: executor.Absent}
}

func (x *watched) Apply(ctx context.Context, tool executor.Tool, op adapter.Op, pre executor.Observed, opt executor.Options) (executor.Applied, error) {
	t := executor.TargetOf(op)
	want, _ := executor.Expect(op)
	x.log.add(x.name, fmt.Sprintf("apply %s %s %s %s keep=%v", tool.ID, t.Op, keyID(t), want, opt.Keep))
	k, ok := op.(adapter.SetKey)
	if !ok {
		return x.Executor.Apply(ctx, tool, op, pre, opt)
	}
	en := manifest.Entry{ID: manifest.EntryID(tool.ID, manifest.OpKey, k.File, k.Path), Tool: tool.ID, Resource: k.Resource.Code,
		Version: k.Resource.Version, Op: manifest.OpKey, Path: k.File, Format: string(k.Format), Key: k.Path, Digest: want, At: fixedNow()}
	if x.journal != nil {
		in, err := x.journal.Intend(manifest.Intent{Kind: manifest.Write, Entry: en, After: want})
		if err != nil {
			return executor.Applied{}, err
		}
		if en, err = x.journal.Commit(in); err != nil {
			return executor.Applied{}, err
		}
	}
	x.keys.mu.Lock()
	defer x.keys.mu.Unlock()
	if x.keys.byHome[x.home] == nil {
		x.keys.byHome[x.home] = map[string]string{}
	}
	x.keys.byHome[x.home][keyID(t)] = want
	return executor.Applied{Entry: en}, nil
}

func (x *watched) Revert(ctx context.Context, en manifest.Entry) (executor.Reverted, error) {
	x.log.add(x.name, "revert "+en.ID)
	if en.Op != manifest.OpKey {
		return x.Executor.Revert(ctx, en)
	}
	if x.journal != nil {
		in, err := x.journal.Intend(manifest.Intent{Kind: manifest.Remove, Entry: en, Before: en.Digest})
		if err != nil {
			return executor.Reverted{}, err
		}
		if _, err := x.journal.Commit(in); err != nil {
			return executor.Reverted{}, err
		}
	}
	x.keys.mu.Lock()
	defer x.keys.mu.Unlock()
	delete(x.keys.byHome[x.home], keyID(executor.TargetOfEntry(en)))
	return executor.Reverted{Outcome: executor.Removed}, nil
}

func unixOnly(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("file permissions that make a file unreadable are a Unix thing")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
}

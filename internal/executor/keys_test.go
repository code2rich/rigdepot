// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package executor

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/configedit"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/manifest"
)

var (
	jiraRef = resource.Ref{Kind: resource.KindMCP, Code: "jira"}
	ourKey  = brand.Prefix + "jira"
	// The tools as their adapters report them: Claude Code's ~/.claude.json lies outside its
	// configuration root and is named by the profile, so it is a declared place of its own.
	claudeKeys = Tool{ID: "claude-code", Roots: map[string]string{"config": "~/.claude", "mcp": "~/.claude.json"}}
	opencode   = Tool{ID: "opencode", Roots: map[string]string{"config": "~/.config/opencode"}}
	codex      = Tool{ID: "codex", Roots: map[string]string{"config": "~/.codex"}}
)

// The person's ~/.claude.json, as Claude Code writes it: strict JSON, two spaces, a server of
// their own.
const claudeJSON = `{
  "numStartups": 42,
  "theme": "dark",
  "mcpServers": {
    "docs": {
      "type": "http",
      "url": "https://mcp.example.com/docs"
    }
  },
  "userID": "<placeholder: user id>"
}
`

func jiraServer(args ...any) map[string]any {
	return map[string]any{
		"type":    "stdio",
		"command": "/opt/example/bin/" + brand.CLI,
		"args":    append([]any{"mcp", "run", "jira"}, args...),
		"env":     map[string]any{},
	}
}

func claudeOp(value any) adapter.SetKey {
	return adapter.SetKey{Resource: jiraRef, File: "~/.claude.json", Format: adapter.JSON, Path: []string{"mcpServers", ourKey}, Value: value}
}

// keyEnv is a home directory with the person's ~/.claude.json in it.
func keyEnv(t *testing.T, content string, perm os.FileMode) *env {
	t.Helper()
	h := t.TempDir()
	p := filepath.Join(h, ".claude.json")
	if err := os.WriteFile(p, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, perm); err != nil {
		t.Fatal(err)
	}
	return openEnv(t, h)
}

func (e *env) applyKey(t *testing.T, tool Tool, op adapter.SetKey) Applied {
	t.Helper()
	res, err := e.ex.Apply(ctx, tool, op, e.ex.Observe(TargetOf(op)), Options{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return res
}

func (e *env) read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.home, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSetKeyWritesTheKeyAndRecordsHowToTakeItOut(t *testing.T) {
	e := keyEnv(t, claudeJSON, 0o600)
	op := claudeOp(jiraServer())
	res := e.applyKey(t, claudeKeys, op)

	got := e.read(t, ".claude.json")
	if v, ok, err := configedit.Get(adapter.JSON, []byte(got), op.Path); err != nil || !ok || keyDigestOf(t, v) != keyDigestOf(t, op.Value) {
		t.Fatalf("the key does not read back: %v %v\n%s", ok, err, got)
	}
	if !strings.HasPrefix(got, claudeJSON[:strings.Index(claudeJSON, `      "url"`)]) {
		t.Errorf("bytes before the new key changed:\n%s", got)
	}
	en := res.Entry
	want := manifest.Entry{
		ID: manifest.EntryID("claude-code", manifest.OpKey, "~/.claude.json", op.Path), Tool: "claude-code", Resource: "jira",
		Op: manifest.OpKey, Path: "~/.claude.json", Format: "json", Key: op.Path, Digest: keyDigestOf(t, op.Value),
	}
	if en.ID != want.ID || en.Path != want.Path || en.Format != want.Format || strings.Join(en.Key, "/") != strings.Join(want.Key, "/") || en.Digest != want.Digest || len(en.Created) != 0 {
		t.Errorf("entry %+v\nwant  %+v", en, want)
	}
	if en.Edit == nil || en.Edit.Created != 1 || !strings.Contains(got, string(en.Edit.New)) {
		t.Errorf("entry records no usable edit: %+v", en.Edit)
	}
	// The file as it was, in backup/<time>/, named in the entry.
	if en.Backup == "" || !strings.HasPrefix(en.Backup, home.BackupDir+"/") {
		t.Fatalf("backup %q", en.Backup)
	}
	if b, err := os.ReadFile(e.data.Join(filepath.FromSlash(en.Backup))); err != nil || string(b) != claudeJSON {
		t.Errorf("the backup does not hold the original file: %v", err)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(filepath.Join(e.home, ".claude.json")); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("the file's permissions were not kept: %v %v", fi.Mode(), err)
		}
	}
	if got := e.entries(t); len(got) != 1 || got[0].Edit == nil {
		t.Fatalf("manifest %+v", got)
	}
	e.assertNoIntents(t)
	if o := e.ex.Observe(TargetOfEntry(en)); o.State != Present || o.Digest != en.Digest {
		t.Errorf("Observe after Apply = %+v", o)
	}
}

func TestRevertOfAnUntouchedFileRestoresItByteForByte(t *testing.T) {
	for name, content := range map[string]string{
		"LF":                      claudeJSON,
		"CRLF and BOM":            "\ufeff" + strings.ReplaceAll(claudeJSON, "\n", "\r\n"),
		"no newline at the end":   strings.TrimSuffix(claudeJSON, "\n"),
		"no mcpServers yet":       "{\n  \"numStartups\": 1\n}\n",
		"one line, a key changed": `{"numStartups":1,"mcpServers":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			e := keyEnv(t, content, 0o644)
			before := snapshot(t, e.home)
			res := e.applyKey(t, claudeKeys, claudeOp(jiraServer()))
			if name == "one line, a key changed" {
				// The tool bumped a counter before our key: our bytes moved, unchanged.
				cur := strings.Replace(e.read(t, ".claude.json"), `"numStartups":1`, `"numStartups":12`, 1)
				writeFile(t, filepath.Join(e.home, ".claude.json"), cur)
				before[".claude.json"] = strings.Replace(content, `"numStartups":1`, `"numStartups":12`, 1)
			}
			out, err := e.ex.Revert(ctx, res.Entry)
			if err != nil || out.Outcome != Removed {
				t.Fatalf("Revert = %+v, %v", out, err)
			}
			assertSame(t, before, snapshot(t, e.home), "after undoing")
			if len(e.entries(t)) != 0 {
				t.Error("the entry is still there")
			}
			e.assertNoIntents(t)
		})
	}
}

func TestRevertAfterTheToolReorderedTheFileIsSemantic(t *testing.T) {
	e := keyEnv(t, claudeJSON, 0o644)
	res := e.applyKey(t, claudeKeys, claudeOp(jiraServer()))
	// Claude Code saves its state: other order, other indentation, a counter changed.
	var doc map[string]any
	if err := json.Unmarshal([]byte(e.read(t, ".claude.json")), &doc); err != nil {
		t.Fatal(err)
	}
	doc["numStartups"] = 43
	rewritten, _ := json.MarshalIndent(doc, "", "\t")
	writeFile(t, filepath.Join(e.home, ".claude.json"), string(rewritten))

	if out, err := e.ex.Revert(ctx, res.Entry); err != nil || out.Outcome != Removed {
		t.Fatalf("Revert = %+v, %v", out, err)
	}
	got, err := configedit.Decode(adapter.JSON, []byte(e.read(t, ".claude.json")))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := configedit.Decode(adapter.JSON, []byte(claudeJSON))
	want["numStartups"] = int64(43)
	if g, w := mustJSON(t, got), mustJSON(t, want); g != w {
		t.Errorf("after undoing:\n got  %s\n want %s", g, w)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAFileTheKeyCreatedGoesWithItsDirectories(t *testing.T) {
	e := newEnv(t)
	before := snapshot(t, e.home)
	op := adapter.SetKey{Resource: jiraRef, File: "~/.config/opencode/opencode.jsonc", Format: adapter.JSONC,
		Path: []string{"mcp", "servers", ourKey}, Value: map[string]any{"type": "local", "command": []any{"x"}}}
	res := e.applyKey(t, opencode, op)
	want := []string{"~/.config", "~/.config/opencode", "~/.config/opencode/opencode.jsonc"}
	if strings.Join(res.Entry.Created, " ") != strings.Join(want, " ") || res.Entry.Backup != "" {
		t.Fatalf("entry %+v", res.Entry)
	}
	if out, err := e.ex.Revert(ctx, res.Entry); err != nil || out.Outcome != Removed {
		t.Fatalf("Revert = %+v, %v", out, err)
	}
	assertSame(t, before, snapshot(t, e.home), "after undoing a key in a file it created")
}

func TestAFileTheKeyCreatedStaysWhenThePersonAddedToIt(t *testing.T) {
	e := newEnv(t)
	op := adapter.SetKey{Resource: jiraRef, File: "~/.config/opencode/opencode.jsonc", Format: adapter.JSONC,
		Path: []string{"mcp", "servers", ourKey}, Value: map[string]any{"type": "local"}}
	res := e.applyKey(t, opencode, op)
	p := filepath.Join(e.home, ".config", "opencode", "opencode.jsonc")
	cur, _ := os.ReadFile(p)
	theirs, _, err := configedit.Set(adapter.JSONC, cur, []string{"theme"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, string(theirs))
	if _, err := e.ex.Revert(ctx, res.Entry); err != nil {
		t.Fatal(err)
	}
	doc, err := configedit.Decode(adapter.JSONC, []byte(e.read(t, ".config/opencode/opencode.jsonc")))
	if err != nil || mustJSON(t, doc) != `{"theme":"system"}` {
		t.Fatalf("after undoing: %v %v", doc, err)
	}
}

func TestRevertKeepsAValueChangedByHand(t *testing.T) {
	e := keyEnv(t, claudeJSON, 0o644)
	op := claudeOp(jiraServer())
	res := e.applyKey(t, claudeKeys, op)
	p := filepath.Join(e.home, ".claude.json")
	cur, _ := os.ReadFile(p)
	changed, _, err := configedit.Set(adapter.JSON, cur, op.Path, jiraServer("--verbose"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, string(changed))

	out, err := e.ex.Revert(ctx, res.Entry)
	if err != nil || out.Outcome != RemovedKept || out.Kept == "" {
		t.Fatalf("Revert = %+v, %v", out, err)
	}
	kept, err := os.ReadFile(e.data.Join(filepath.FromSlash(out.Kept)))
	if err != nil || !strings.Contains(string(kept), "--verbose") {
		t.Errorf("kept %q: %v", kept, err)
	}
	if _, ok, _ := configedit.Get(adapter.JSON, []byte(e.read(t, ".claude.json")), op.Path); ok {
		t.Error("the key is still there")
	}
}

func TestRevertOfAKeyThatIsGoneOnlyDropsTheEntry(t *testing.T) {
	e := keyEnv(t, claudeJSON, 0o644)
	op := claudeOp(jiraServer())
	res := e.applyKey(t, claudeKeys, op)
	p := filepath.Join(e.home, ".claude.json")
	cur, _ := os.ReadFile(p)
	without, err := configedit.Delete(adapter.JSON, cur, op.Path)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, string(without))
	before := snapshot(t, e.home)
	if out, err := e.ex.Revert(ctx, res.Entry); err != nil || out.Outcome != AlreadyGone {
		t.Fatalf("Revert = %+v, %v", out, err)
	}
	assertSame(t, before, snapshot(t, e.home), "undoing a key that is gone")
	if len(e.entries(t)) != 0 {
		t.Error("the entry is still there")
	}
}

func TestAFileThatDoesNotParseIsNotWritten(t *testing.T) {
	e := keyEnv(t, "{\n  \"numStartups\": 42,\n  \"mcpServers\": {\n", 0o644) // cut off
	before := snapshot(t, e.home)
	op := claudeOp(jiraServer())
	pre := e.ex.Observe(TargetOf(op))
	if pre.State != Unknown || !errors.Is(pre.Err, configedit.ErrSyntax) {
		t.Fatalf("Observe = %+v, want unknown", pre)
	}
	for _, p := range []Observed{pre, {State: Absent}} {
		if _, err := e.ex.Apply(ctx, claudeKeys, op, p, Options{}); !errors.Is(err, ErrUnreadable) {
			t.Errorf("Apply with plan %v = %v, want ErrUnreadable", p.State, err)
		}
	}
	assertSame(t, before, snapshot(t, e.home), "a file that does not parse was written")
	if _, err := os.Stat(e.data.Join(home.BackupDir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a backup was made of a file that is not written: %v", err)
	}
	e.assertNoIntents(t)
	// Undoing an entry in it is refused as well.
	en := manifest.Entry{ID: "k", Tool: "claude-code", Resource: "jira", Op: manifest.OpKey, Path: "~/.claude.json", Format: "json", Key: op.Path}
	if _, err := e.ex.Revert(ctx, en); !errors.Is(err, ErrUnreadable) {
		t.Errorf("Revert = %v, want ErrUnreadable", err)
	}
	assertSame(t, before, snapshot(t, e.home), "undoing in a file that does not parse")
}

func TestApplyRefusesAKeyThatChangedSinceThePlan(t *testing.T) {
	e := keyEnv(t, claudeJSON, 0o644)
	op := claudeOp(jiraServer())
	pre := e.ex.Observe(TargetOf(op)) // absent
	// The person adds an entry of that name before we write.
	p := filepath.Join(e.home, ".claude.json")
	theirs, _, err := configedit.Set(adapter.JSON, []byte(claudeJSON), op.Path, map[string]any{"type": "http", "url": "https://mcp.example.com/mine"})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, string(theirs))
	before := snapshot(t, e.home)
	if _, err := e.ex.Apply(ctx, claudeKeys, op, pre, Options{}); !errors.Is(err, ErrChanged) {
		t.Fatalf("Apply = %v, want ErrChanged", err)
	}
	assertSame(t, before, snapshot(t, e.home), "a key that changed since the plan was overwritten")
	e.assertNoIntents(t)
}

func TestAFileRewrittenWhileItIsEditedIsNotReplaced(t *testing.T) {
	e := keyEnv(t, claudeJSON, 0o644)
	p := filepath.Join(e.home, ".claude.json")
	rewritten := strings.Replace(claudeJSON, `"numStartups": 42`, `"numStartups": 43`, 1)
	e.ex.hook = func(step string) error {
		if step == stepStaged {
			writeFile(t, p, rewritten) // the tool saves its state right now
		}
		return nil
	}
	op := claudeOp(jiraServer())
	if _, err := e.ex.Apply(ctx, claudeKeys, op, e.ex.Observe(TargetOf(op)), Options{}); !errors.Is(err, ErrChanged) {
		t.Fatalf("Apply = %v, want ErrChanged", err)
	}
	if got := e.read(t, ".claude.json"); got != rewritten {
		t.Errorf("the tool's own write was lost:\n%s", got)
	}
	if l := leftoversIn(t, e.home); len(l) != 0 {
		t.Errorf("left behind: %v", l)
	}
	e.assertNoIntents(t)
	if len(e.entries(t)) != 0 {
		t.Error("an entry was recorded")
	}
}

// leftoversIn lists temporary files next to ~/.claude.json.
func leftoversIn(t *testing.T, h string) []string {
	t.Helper()
	entries, err := os.ReadDir(h)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, en := range entries {
		if strings.Contains(en.Name(), ".tmp-") || strings.Contains(en.Name(), ".old-") {
			out = append(out, en.Name())
		}
	}
	return out
}

func TestKeyTargetsThatAreAnAdapterDefectAreRefused(t *testing.T) {
	e := keyEnv(t, claudeJSON, 0o644)
	before := snapshot(t, e.home)
	ok := claudeOp(jiraServer())
	cases := map[string]struct {
		tool Tool
		op   func(adapter.SetKey) adapter.SetKey
	}{
		"key without the prefix":       {claudeKeys, func(o adapter.SetKey) adapter.SetKey { o.Path = []string{"mcpServers", "jira"}; return o }},
		"prefix on a parent only":      {claudeKeys, func(o adapter.SetKey) adapter.SetKey { o.Path = []string{ourKey, "x"}; return o }},
		"empty key element":            {claudeKeys, func(o adapter.SetKey) adapter.SetKey { o.Path = []string{"", ourKey}; return o }},
		"file the tool did not name":   {claude, func(o adapter.SetKey) adapter.SetKey { return o }},
		"file next to a declared file": {claudeKeys, func(o adapter.SetKey) adapter.SetKey { o.File = "~/.claude.json.bak"; return o }},
		"unknown format":               {claudeKeys, func(o adapter.SetKey) adapter.SetKey { o.Format = "yaml"; return o }},
		"value of a type not allowed":  {claudeKeys, func(o adapter.SetKey) adapter.SetKey { o.Value = []string{"x"}; return o }},
		"path with ..":                 {claudeKeys, func(o adapter.SetKey) adapter.SetKey { o.File = "~/.claude/../.claude.json"; return o }},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := e.ex.Apply(ctx, c.tool, c.op(ok), Observed{State: Absent}, Options{})
			var d *DefectError
			if !errors.As(err, &d) {
				t.Fatalf("Apply = %v, want a defect", err)
			}
		})
	}
	assertSame(t, before, snapshot(t, e.home), "a defective operation changed something")
	// The single MCP entry is ours too.
	single := ok
	single.Path = []string{"mcpServers", brand.MCPEntry}
	e.applyKey(t, claudeKeys, single)
}

func TestASymbolicLinkToTheConfigurationIsLeftAlone(t *testing.T) {
	h := t.TempDir()
	writeFile(t, filepath.Join(h, "dotfiles", "claude.json"), claudeJSON)
	if err := os.Symlink(filepath.Join("dotfiles", "claude.json"), filepath.Join(h, ".claude.json")); err != nil {
		t.Skip("cannot make symbolic links here:", err)
	}
	e := openEnv(t, h)
	before := snapshot(t, h)
	op := claudeOp(jiraServer())
	pre := e.ex.Observe(TargetOf(op))
	if pre.State != Unknown || !errors.Is(pre.Err, ErrNotRegularFile) {
		t.Fatalf("Observe = %+v", pre)
	}
	if _, err := e.ex.Apply(ctx, claudeKeys, op, pre, Options{}); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("Apply = %v", err)
	}
	assertSame(t, before, snapshot(t, h), "a linked configuration file was replaced")
	if fi, err := os.Lstat(filepath.Join(h, ".claude.json")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link is gone: %v", err)
	}
}

const codexTOML = "# Codex configuration\r\nmodel = \"example-model\"\r\n\r\n[mcp_servers.docs]\r\ncommand = \"npx\"\r\n"

func TestATOMLTableIsAppendedAndTakenOut(t *testing.T) {
	h := t.TempDir()
	writeFile(t, filepath.Join(h, ".codex", "config.toml"), codexTOML)
	e := openEnv(t, h)
	before := snapshot(t, h)
	op := adapter.SetKey{Resource: jiraRef, File: "~/.codex/config.toml", Format: adapter.TOML, Path: []string{"mcp_servers", ourKey},
		Value: map[string]any{"command": "/opt/example/bin/" + brand.CLI, "args": []any{"mcp", "run", "jira"}, "env": map[string]any{"JIRA_URL": "https://jira.example.com"}}}
	res := e.applyKey(t, codex, op)
	got := e.read(t, ".codex/config.toml")
	if !strings.HasPrefix(got, codexTOML) || !strings.Contains(got, "\r\n["+"mcp_servers."+ourKey+"]\r\n") {
		t.Errorf("written:\n%q", got)
	}
	if _, err := e.ex.Revert(ctx, res.Entry); err != nil {
		t.Fatal(err)
	}
	assertSame(t, before, snapshot(t, h), "after undoing a TOML table")
}

func TestAnInlineTableIsNotRewritten(t *testing.T) {
	h := t.TempDir()
	content := "model = \"example-model\"\nmcp_servers = { docs = { command = \"npx\" } }\n"
	writeFile(t, filepath.Join(h, ".codex", "config.toml"), content)
	e := openEnv(t, h)
	before := snapshot(t, h)
	op := adapter.SetKey{Resource: jiraRef, File: "~/.codex/config.toml", Format: adapter.TOML, Path: []string{"mcp_servers", ourKey}, Value: map[string]any{"command": "x"}}
	if _, err := e.ex.Apply(ctx, codex, op, e.ex.Observe(TargetOf(op)), Options{}); !errors.Is(err, configedit.ErrInlineTable) {
		t.Fatalf("Apply = %v, want ErrInlineTable", err)
	}
	assertSame(t, before, snapshot(t, h), "an inline table was rewritten")
	e.assertNoIntents(t)
}

func TestSeveralKeysInOneFileBackUpTheOriginalAndUndoInReverse(t *testing.T) {
	e := keyEnv(t, claudeJSON, 0o644)
	before := snapshot(t, e.home)
	var entries []manifest.Entry
	for _, name := range []string{"jira", "wiki"} {
		op := claudeOp(jiraServer())
		op.Resource = resource.Ref{Kind: resource.KindMCP, Code: name}
		op.Path = []string{"mcpServers", brand.Prefix + name}
		entries = append(entries, e.applyKey(t, claudeKeys, op).Entry)
	}
	if entries[0].Backup != entries[1].Backup {
		t.Errorf("backups %q and %q; one run keeps one copy, the first", entries[0].Backup, entries[1].Backup)
	}
	if b, _ := os.ReadFile(e.data.Join(filepath.FromSlash(entries[1].Backup))); string(b) != claudeJSON {
		t.Error("the backup is not the file as it was before the run")
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if _, err := e.ex.Revert(ctx, entries[i]); err != nil {
			t.Fatal(err)
		}
	}
	assertSame(t, before, snapshot(t, e.home), "after undoing both keys")
}

func TestReplacingAValueRecordsNoEditAndUndoRemovesTheKey(t *testing.T) {
	e := keyEnv(t, claudeJSON, 0o644)
	op := claudeOp(jiraServer())
	e.applyKey(t, claudeKeys, op)
	newer := claudeOp(jiraServer("--read-only"))
	res := e.applyKey(t, claudeKeys, newer)
	if res.Entry.Edit != nil {
		t.Errorf("a replacement recorded an edit: %+v", res.Entry.Edit)
	}
	if _, err := e.ex.Revert(ctx, res.Entry); err != nil {
		t.Fatal(err)
	}
	got, _ := configedit.Decode(adapter.JSON, []byte(e.read(t, ".claude.json")))
	want, _ := configedit.Decode(adapter.JSON, []byte(claudeJSON))
	if mustJSON(t, got) != mustJSON(t, want) {
		t.Errorf("after undoing:\n%s", e.read(t, ".claude.json"))
	}
}

func TestOverwritingAKeyWithKeepCopiesTheValueFirst(t *testing.T) {
	e := keyEnv(t, claudeJSON, 0o644)
	op := claudeOp(jiraServer())
	e.applyKey(t, claudeKeys, op)
	newer := claudeOp(jiraServer("--read-only"))
	res, err := e.ex.Apply(ctx, claudeKeys, newer, e.ex.Observe(TargetOf(newer)), Options{Keep: true})
	if err != nil || res.Kept == "" {
		t.Fatalf("Apply = %+v, %v", res, err)
	}
	if b, err := os.ReadFile(e.data.Join(filepath.FromSlash(res.Kept))); err != nil || strings.Contains(string(b), "--read-only") || !strings.Contains(string(b), `"jira"`) {
		t.Errorf("kept %q: %v", b, err)
	}
}

func TestAPreviewWithoutALocalDataDirectoryMakesNoBackup(t *testing.T) {
	h := t.TempDir()
	writeFile(t, filepath.Join(h, ".claude.json"), claudeJSON)
	ex, err := New(Roots{Home: h}, State{Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	op := claudeOp(jiraServer())
	res, err := ex.Apply(ctx, claudeKeys, op, Observed{State: Absent}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Entry.Backup != "" || res.Entry.Edit == nil {
		t.Errorf("entry %+v", res.Entry)
	}
	if _, err := os.Stat(filepath.Join(h, brand.Home)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a preview created the local data directory: %v", err)
	}
}

func TestExpectOfAKeyIsWhatReadsBack(t *testing.T) {
	value := map[string]any{"n": 1, "big": float64(1 << 60), "f": 0.5, "s": "<a&b>", "l": []any{true, int64(-3)}, "m": map[string]any{}}
	for _, format := range []adapter.Format{adapter.JSON, adapter.JSONC, adapter.TOML} {
		out, _, err := configedit.Set(format, nil, []string{"x", ourKey}, value)
		if err != nil {
			t.Fatal(err)
		}
		back, ok, err := configedit.Get(format, out, []string{"x", ourKey})
		if err != nil || !ok {
			t.Fatal(err)
		}
		if keyDigestOf(t, back) != keyDigestOf(t, value) {
			t.Errorf("%s: the digest of the value read back differs", format)
		}
	}
	if !strings.HasPrefix(keyDigestOf(t, "x"), "sha256:") {
		t.Errorf("keyDigest = %q", keyDigestOf(t, "x"))
	}
}

func TestAnInterruptedKeyEditIsSettled(t *testing.T) {
	e := keyEnv(t, claudeJSON, 0o644)
	op := claudeOp(jiraServer())
	// A write whose intent was logged and whose temporary file was left behind by a crash.
	in, err := e.locked.Intend(manifest.Intent{Kind: manifest.Write, Entry: manifest.Entry{
		ID: manifest.EntryID("claude-code", manifest.OpKey, "~/.claude.json", op.Path), Tool: "claude-code", Resource: "jira",
		Op: manifest.OpKey, Path: "~/.claude.json", Format: "json", Key: op.Path, Digest: keyDigestOf(t, op.Value),
	}, After: keyDigestOf(t, op.Value)})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(e.home, sibling(".claude.json", "tmp", in.ID)), "{half")
	r, err := e.locked.Recover(e.ex.Settle)
	if err != nil || len(r.Dropped) != 1 {
		t.Fatalf("Recover = %+v, %v", r, err)
	}
	if l := leftoversIn(t, e.home); len(l) != 0 {
		t.Errorf("left behind: %v", l)
	}
	if e.read(t, ".claude.json") != claudeJSON {
		t.Error("the file changed")
	}
}

func TestByteExactUndoNeedsTheBytesWhereTheyWere(t *testing.T) {
	// The recorded bytes found once elsewhere count only if the result means the file without
	// the key: a record that does not fit falls back to removing the key by its meaning.
	e := keyEnv(t, claudeJSON, 0o644)
	res := e.applyKey(t, claudeKeys, claudeOp(jiraServer()))
	bad := res.Entry
	bad.Edit = &manifest.KeyEdit{Offset: 3, Old: []byte("zzz"), New: []byte(`"theme"`), Created: 1}
	if _, err := e.ex.Revert(ctx, bad); err != nil {
		t.Fatal(err)
	}
	got := e.read(t, ".claude.json")
	if !bytes.Contains([]byte(got), []byte(`"theme": "dark"`)) || strings.Contains(got, "zzz") || strings.Contains(got, ourKey) {
		t.Errorf("after undoing with a record that does not fit:\n%s", got)
	}
}

// keyDigestOf is Expect for a SetKey of v.
func keyDigestOf(t *testing.T, v any) string {
	t.Helper()
	d, err := Expect(adapter.SetKey{Value: v})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

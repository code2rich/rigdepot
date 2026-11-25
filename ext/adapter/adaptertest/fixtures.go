// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package adaptertest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/brand"
)

// requiredCases are the homes every adapter has fixtures for. Each guards something the product
// promises about any tool; a tool with situations of its own adds cases next to them.
var requiredCases = []string{"fresh", "user-config", "crlf-bom", "broken-config", "occupied"}

// The names of the cases whose own assertions the suite knows.
const (
	caseBroken   = "broken-config"
	caseOccupied = "occupied"
)

// caseSpec is case.json: what the computer looks like apart from the files in the home directory.
// Every field is optional.
type caseSpec struct {
	// GOOS is the operating system the adapter is told about. Default: the one the tests run on.
	GOOS string `json:"goos"`
	// Env holds the environment variables the adapter can read. A value that starts with "~/"
	// is a path inside the fixture home.
	Env map[string]string `json:"env"`
	// Bins lists the executables found on the PATH. Default: every one the profile looks for.
	Bins *[]string `json:"bins"`
	// VersionOutput is what the tool prints when asked for its version. Default: the version
	// the profile was verified against.
	VersionOutput *string `json:"versionOutput"`
}

// fixtureCase is one home of the fixtures.
type fixtureCase struct {
	name string
	dir  string // testdata/homes/<name>
	spec caseSpec
}

// loadCases reads testdata/homes. Every case is a directory with a README.md that says where
// its files come from and what it shows, an optional case.json, and the home itself in home/
// (which may be missing: an empty home).
func loadCases(dir string) ([]fixtureCase, []string) {
	var problems []string
	entries, err := os.ReadDir(filepath.Join(dir, "homes"))
	if err != nil {
		return nil, []string{fmt.Sprintf("the fixtures need a homes/ directory: %v", err)}
	}
	var cases []fixtureCase
	have := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			problems = append(problems, fmt.Sprintf("homes/%s: every case is a directory", e.Name()))
			continue
		}
		c := fixtureCase{name: e.Name(), dir: filepath.Join(dir, "homes", e.Name())}
		if _, err := os.Stat(filepath.Join(c.dir, "README.md")); err != nil {
			problems = append(problems, fmt.Sprintf("homes/%s has no README.md saying which tool version its files come from and what it shows", c.name))
		}
		if data, err := os.ReadFile(filepath.Join(c.dir, "case.json")); err == nil {
			dec := json.NewDecoder(bytes.NewReader(data))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&c.spec); err != nil {
				problems = append(problems, fmt.Sprintf("homes/%s/case.json: %v", c.name, err))
				continue
			}
		} else if !os.IsNotExist(err) {
			problems = append(problems, fmt.Sprintf("homes/%s/case.json: %v", c.name, err))
			continue
		}
		have[c.name] = true
		cases = append(cases, c)
	}
	for _, name := range requiredCases {
		if !have[name] {
			problems = append(problems, fmt.Sprintf("homes/%s is missing; every adapter has the cases %s", name, strings.Join(requiredCases, ", ")))
		}
	}
	return cases, problems
}

// toolEnv is the computer an adapter sees for one home: Env plus a record of the commands it ran.
type toolEnv struct {
	env      adapter.Env
	commands *[]string
}

// newToolEnv makes the Env for a home on disk. ReadFile and Stat only reach inside the home.
func newToolEnv(home string, spec caseSpec, p adapter.Profile) toolEnv {
	goos := spec.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	bins := p.Detect.Bins
	if spec.Bins != nil {
		bins = *spec.Bins
	}
	version := ""
	if p.Verified != nil {
		version = p.Verified.Version
	}
	if spec.VersionOutput != nil {
		version = *spec.VersionOutput
	}
	commands := &[]string{}
	resolve := func(p string) (string, error) {
		var rel string
		switch {
		case p == "~":
			rel = "."
		case strings.HasPrefix(p, "~/"):
			rel = p[2:]
		default:
			h := strings.TrimSuffix(filepath.ToSlash(home), "/") + "/"
			r, ok := strings.CutPrefix(path.Clean(p), h)
			if !ok {
				return "", fmt.Errorf("%s: %w (outside the home directory)", p, fs.ErrPermission)
			}
			rel = r
		}
		rel = path.Clean(rel)
		if rel == ".." || strings.HasPrefix(rel, "../") {
			return "", fmt.Errorf("%s: %w (outside the home directory)", p, fs.ErrPermission)
		}
		return filepath.Join(home, filepath.FromSlash(rel)), nil
	}
	return toolEnv{
		commands: commands,
		env: adapter.Env{
			GOOS: goos,
			Home: home,
			Getenv: func(key string) string {
				v := spec.Env[key]
				if v == "~" || strings.HasPrefix(v, "~/") {
					v = filepath.Join(home, filepath.FromSlash(strings.TrimPrefix(strings.TrimPrefix(v, "~"), "/")))
				}
				return v
			},
			ReadFile: func(p string) ([]byte, error) {
				full, err := resolve(p)
				if err != nil {
					return nil, err
				}
				return os.ReadFile(full)
			},
			Stat: func(p string) (fs.FileInfo, error) {
				full, err := resolve(p)
				if err != nil {
					return nil, err
				}
				return os.Stat(full)
			},
			LookPath: func(name string) (string, error) {
				for _, b := range bins {
					if b == name {
						return "/usr/local/bin/" + name, nil
					}
				}
				return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
			},
			Version: func(_ context.Context, bin string, args ...string) (string, error) {
				*commands = append(*commands, strings.Join(append([]string{bin}, args...), " "))
				return version, nil
			},
		},
	}
}

// The artifacts every adapter is tested with: one of each shape, an MCP server for each
// transport, and one that refers to a personal secret. The suite passes the ones the adapter's
// capabilities take, the way the core does after rendering.
func standardArtifacts(caps adapter.Capabilities) []adapter.Artifact {
	name := func(code string) string { return brand.Prefix + code }
	var out []adapter.Artifact
	if caps.Skills {
		out = append(out, adapter.SkillDir{
			Resource: resource.Ref{Kind: resource.KindSkill, Code: "contract-skill", Version: 3},
			DirName:  name("contract-skill"),
			Files: []adapter.File{
				{Path: "SKILL.md", Data: []byte("---\nname: " + name("contract-skill") + "\ndescription: Checks that the adapter contract holds. Use it when asked to.\n---\n\n# Contract skill\n\nRun `scripts/check.sh` and report what it prints.\n")},
				{Path: "scripts/check.sh", Data: []byte("#!/bin/sh\necho ok\n"), Exec: true},
			},
		})
	}
	if caps.Commands {
		out = append(out, adapter.Command{
			Resource:    resource.Ref{Kind: resource.KindWorkflow, Code: "contract-command", Version: 1},
			CmdName:     name("contract-command"),
			Description: "Walks through the contract, step by step",
			Body:        "1. Read the plan.\n2. Check every step.\n",
		})
	}
	if caps.Agents {
		out = append(out, adapter.SubAgent{
			Resource:    resource.Ref{Kind: resource.KindAgent, Code: "contract-agent", Version: 1},
			AgentName:   name("contract-agent"),
			Description: "Reviews changes against the contract",
			Body:        "You review changes. Say what breaks the contract and why.\n",
		})
	}
	if caps.MCP.Stdio {
		out = append(out, adapter.MCPServer{
			Resource:  resource.Ref{Kind: resource.KindMCP, Code: "contract-stdio", Version: 2},
			EntryName: name("contract-stdio"),
			Transport: adapter.Stdio,
			Command:   "contract-mcp",
			Args:      []string{"--stdio", "--log-level", "info"},
			Env:       map[string]adapter.Value{"CONTRACT_MODE": {Literal: "read-only"}},
		})
	}
	if caps.MCP.HTTP {
		out = append(out,
			adapter.MCPServer{
				Resource:  resource.Ref{Kind: resource.KindMCP, Code: "contract-http", Version: 1},
				EntryName: name("contract-http"),
				Transport: adapter.HTTP,
				URL:       "https://mcp.example.com/contract",
				Headers:   map[string]adapter.Value{"X-Contract": {Literal: "yes"}},
			},
			adapter.MCPServer{
				Resource:  resource.Ref{Kind: resource.KindMCP, Code: "contract-secret", Version: 1},
				EntryName: name("contract-secret"),
				Transport: adapter.HTTP,
				URL:       "https://secret.example.com/mcp",
				Headers:   map[string]adapter.Value{"Authorization": {Literal: "Bearer ", Secret: "CONTRACT_TOKEN"}},
			})
	}
	return out
}

// describePlan writes a plan as text, one line per operation and skip. With details, the
// content of every operation is in it as a digest, so that two plans that differ anywhere
// describe differently.
func describePlan(p adapter.Plan, details bool) string {
	var b strings.Builder
	for _, op := range p.Ops {
		switch o := op.(type) {
		case adapter.WriteTree:
			names := make([]string, 0, len(o.Files))
			for _, f := range o.Files {
				n := f.Path
				if f.Exec {
					n += "*"
				}
				if details {
					n += "=" + digest(f.Data)
				}
				names = append(names, n)
			}
			sort.Strings(names)
			fmt.Fprintf(&b, "op write-tree %s (%s): %s\n", o.Dir, o.Resource, strings.Join(names, ", "))
		case adapter.WriteFile:
			fmt.Fprintf(&b, "op write-file %s (%s)", o.Path, o.Resource)
			if details {
				fmt.Fprintf(&b, " =%s", digest(o.Data))
			}
			b.WriteString("\n")
		case adapter.SetKey:
			value, err := canonical(o.Value)
			if err != nil {
				value = fmt.Sprintf("(cannot be written as JSON: %v)", err)
			}
			k, _ := json.Marshal(o.Path)
			fmt.Fprintf(&b, "op set-key %s %s %s (%s) = %s\n", o.File, o.Format, k, o.Resource, value)
		case adapter.Exec:
			a, _ := json.Marshal(o.Argv)
			u, _ := json.Marshal(o.Undo)
			fmt.Fprintf(&b, "op exec %s undo %s (%s) effects %v\n", a, u, o.Resource, o.Effects)
		default:
			fmt.Fprintf(&b, "op %T\n", op)
		}
	}
	for _, s := range p.Skips {
		fmt.Fprintf(&b, "skip %s %s: %s\n", s.Resource, s.Reason, s.Detail)
	}
	return b.String()
}

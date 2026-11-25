// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package sync

import (
	"cmp"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/desired"
)

// Degraded is a resource delivered to a tool in a fallback form.
type Degraded struct {
	Resource resource.Ref
	// Note is the message key of the explanation, such as "workflow.as_skill".
	Note string
}

// rendered is every resource of the desired state, rendered for one tool.
type rendered struct {
	artifacts []adapter.Artifact
	degraded  []Degraded
	// skips are the resources no form of their kind fits the tool.
	skips []adapter.Skip
	// failed are the resources whose rendering failed: unknown for this tool.
	failed map[string]error
}

// renderOptions are the options every resource is rendered with on this computer: the product's
// prefix and command, and the local data directory written as "~/…" when it is inside the home
// directory, which is how the AI tools' shells reach it on every system.
func (e *Engine) renderOptions() kind.RenderOptions {
	return kind.RenderOptions{Prefix: brand.Prefix, CLI: brand.CLI, Data: e.dataForAI()}
}

// dataForAI is the local data directory the way the AI can use it in a shell command.
func (e *Engine) dataForAI() string {
	if rel, err := filepath.Rel(e.sys.Home, e.data.Path()); err == nil && filepath.IsLocal(rel) {
		return "~/" + filepath.ToSlash(rel)
	}
	return filepath.ToSlash(e.data.Path())
}

// render picks, for each resource, the first form of its kind that the tool's capabilities
// satisfy and whose Render does not say ErrNotApplicable. That order is the fallback rule.
func render(resources []desired.Resource, caps adapter.Capabilities, opt kind.RenderOptions) rendered {
	out := rendered{failed: map[string]error{}}
	for _, r := range resources {
		if r.Kind == nil {
			out.failed[r.Ref.Code] = errors.New("the resource has no kind")
			continue
		}
		delivered := false
		for _, f := range r.Kind.Forms() {
			if !f.Needs.SatisfiedBy(caps) || f.Render == nil {
				continue
			}
			arts, err := f.Render(r.Spec, caps, opt)
			if errors.Is(err, kind.ErrNotApplicable) {
				continue
			}
			if err == nil {
				err = checkArtifacts(r.Ref, arts)
			}
			if err != nil {
				out.failed[r.Ref.Code] = err
				delivered = true
				break
			}
			out.artifacts = append(out.artifacts, arts...)
			if f.Degraded != "" {
				out.degraded = append(out.degraded, Degraded{Resource: r.Ref, Note: f.Degraded})
			}
			delivered = true
			break
		}
		if !delivered {
			out.skips = append(out.skips, adapter.Skip{Resource: r.Ref, Reason: adapter.Unsupported,
				Detail: fmt.Sprintf("no form of the %s kind fits what the tool takes", r.Ref.Kind)})
		}
	}
	return out
}

// checkArtifacts refuses artifacts that do not belong to the resource they were rendered from:
// the manifest records operations by resource, and an artifact of another resource would be
// recorded, and undone, under the wrong one.
func checkArtifacts(ref resource.Ref, arts []adapter.Artifact) error {
	for _, a := range arts {
		if a == nil {
			return errors.New("the kind rendered a nil artifact")
		}
		if a.Ref().Code != ref.Code {
			return fmt.Errorf("the kind rendered an artifact of %s for %s", a.Ref(), ref)
		}
	}
	return nil
}

// launch rewrites the MCP servers a tool takes natively for the MCP modes in which the
// command-line program starts them, so that no secret has to be in the tool's configuration:
//
//   - MCPLauncher: each server's entry starts "<launcher> mcp run <code>" instead.
//   - MCPSingleEntry: the servers' entries are replaced by one, brand.MCPEntry, that starts
//     "<launcher> mcp serve", the proxy of them all; without any server, there is no entry.
//
// The single entry is recorded under a resource of its own, the kind mcp with the entry's name
// as code, since it belongs to no resource of the repository; behind lists the resources whose
// servers it stands for. A tool that does not take stdio servers keeps the servers as they are:
// there is nothing to start for it.
func launch(arts []adapter.Artifact, caps adapter.Capabilities, opt Options) (out []adapter.Artifact, behind []resource.Ref) {
	if (opt.MCPMode != adapter.MCPLauncher && opt.MCPMode != adapter.MCPSingleEntry) || !caps.MCP.Stdio {
		return arts, nil
	}
	cmd := cmp.Or(opt.Launcher, brand.CLI)
	out = make([]adapter.Artifact, 0, len(arts))
	for _, a := range arts {
		s, ok := a.(adapter.MCPServer)
		if !ok {
			out = append(out, a)
			continue
		}
		behind = append(behind, s.Resource)
		if opt.MCPMode == adapter.MCPLauncher {
			out = append(out, adapter.MCPServer{Resource: s.Resource, EntryName: s.EntryName, Transport: adapter.Stdio,
				Command: cmd, Args: []string{"mcp", "run", s.Resource.Code}})
		}
	}
	if opt.MCPMode != adapter.MCPSingleEntry {
		return out, nil
	}
	if len(behind) > 0 {
		out = append(out, adapter.MCPServer{Resource: SingleEntry, EntryName: brand.MCPEntry, Transport: adapter.Stdio,
			Command: cmd, Args: []string{"mcp", "serve"}})
	}
	return out, behind
}

// SingleEntry is the resource the single entry of MCPSingleEntry is recorded under.
var SingleEntry = resource.Ref{Kind: resource.KindMCP, Code: brand.MCPEntry}

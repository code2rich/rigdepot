// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
	"github.com/code2rich/rigdepot/internal/server/settings"
)

// toolsAPI implements the operations tagged adapters and the command-line policy of the tag cli
// (getCLIPolicy, updateCLIPolicy): the "tool adapters" page. Where each tool keeps what, how it
// falls back and which tool version that was checked against come from the adapters' built-in
// profiles (ext/adapter, registered by builtin/all in the server program too); the switches and
// the minimum version are settings.
type toolsAPI struct {
	settings *settings.Service
	hooks    *hooks
	// adapters are the adapters compiled in; nil means the registry (adapter.All).
	adapters []adapter.Adapter
	// version is this server's version, the newest command line it hands out.
	version string
}

// toolUsage counts who uses what, from the computers' sync reports: the devices (authn, M1-20)
// and what is installed where (delivery). Until those exist the counts are empty.
type toolUsage interface {
	// PeopleByTool counts the people with each tool detected at their last sync, by adapter ID.
	PeopleByTool(ctx context.Context) (map[string]int, error)
	// CLIVersions counts the people by the command-line version of their most recent sync.
	CLIVersions(ctx context.Context) ([]httpapi.VersionCount, error)
}

func (t *toolsAPI) all() []adapter.Adapter {
	if t.adapters != nil {
		return t.adapters
	}
	return adapter.All()
}

var errAdapterNotFound = httpapi.NewProblem(http.StatusNotFound, "adapter.not_found", "This server has no adapter for that tool.")

// adapterTargets renders where each kind of artifact lands in a tool, from its profile: the
// directories and files with the profile's roots at their defaults ("~/.claude/skills/"), the
// key path inside an MCP configuration file, and "skill" as the fallback for slash commands,
// sub-agents and MCP servers in a tool that has no place of its own for them (resource kinds
// then deliver a skill instead; docs/architecture/05-adapters.md). A tool without a skills
// directory has no fallback.
func adapterTargets(p adapter.Profile) (map[string]httpapi.AdapterTarget, error) {
	roots, err := adapter.ResolveRoots(p.Roots, adapter.Env{GOOS: runtime.GOOS, Getenv: func(string) string { return "" }})
	if err != nil {
		return nil, fmt.Errorf("adapter %s: %w", p.ID, err)
	}
	place := func(tmpl string, dir bool) (*string, error) {
		s, err := adapter.ExpandPath(tmpl, roots)
		if err != nil {
			return nil, fmt.Errorf("adapter %s: %w", p.ID, err)
		}
		if dir && !strings.HasSuffix(s, "/") {
			s += "/"
		}
		return &s, nil
	}
	out := map[string]httpapi.AdapterTarget{}
	var fallback *string
	if p.Skills != nil {
		loc, err := place(p.Skills.Dir, true)
		if err != nil {
			return nil, err
		}
		out["skill"] = httpapi.AdapterTarget{Location: loc}
		skill := "skill"
		fallback = &skill
	} else {
		out["skill"] = httpapi.AdapterTarget{}
	}
	for kind, files := range map[string]*adapter.FilesTarget{"command": p.Commands, "agent": p.Agents} {
		if files == nil {
			out[kind] = httpapi.AdapterTarget{Fallback: fallback}
			continue
		}
		loc, err := place(files.Dir, true)
		if err != nil {
			return nil, err
		}
		out[kind] = httpapi.AdapterTarget{Location: loc}
	}
	switch m := p.MCP; {
	case m == nil || len(m.Files) == 0:
		out["mcp"] = httpapi.AdapterTarget{Fallback: fallback}
	default:
		// The last file is the one created when none exists.
		loc, err := place(m.Files[len(m.Files)-1], false)
		if err != nil {
			return nil, err
		}
		target := httpapi.AdapterTarget{Location: loc}
		if len(m.Path) > 0 {
			detail := strings.Join(m.Path, ".")
			target.Detail = &detail
		}
		out["mcp"] = target
	}
	return out, nil
}

func (t *toolsAPI) adapterDoc(a adapter.Adapter, disabled []string, people map[string]int) (httpapi.Adapter, error) {
	p := a.Profile()
	targets, err := adapterTargets(p)
	if err != nil {
		return httpapi.Adapter{}, err
	}
	out := httpapi.Adapter{Id: a.ID(), Name: p.Name, Targets: targets, People: people[a.ID()], Enabled: true}
	for _, id := range disabled {
		if id == a.ID() {
			out.Enabled = false
		}
	}
	if v := p.Verified; v != nil {
		at, err := time.Parse(time.DateOnly, v.At)
		if err != nil {
			return httpapi.Adapter{}, fmt.Errorf("adapter %s: the verification date %q: %w", p.ID, v.At, err)
		}
		out.Verified = &httpapi.Verification{Version: v.Version, At: openapi_types.Date{Time: at}}
	}
	return out, nil
}

func (t *toolsAPI) peopleByTool(ctx context.Context) (map[string]int, error) {
	if t.hooks.toolUsage == nil {
		return nil, nil
	}
	return t.hooks.toolUsage.PeopleByTool(ctx)
}

func (t *toolsAPI) ListAdapters(ctx context.Context, _ httpapi.ListAdaptersRequestObject) (httpapi.ListAdaptersResponseObject, error) {
	actor, ok := callerFrom(ctx)
	if !ok {
		return nil, errSignInRequired
	}
	tools, err := t.settings.Tools(ctx, actor)
	if err != nil {
		return nil, settingsProblem(err)
	}
	people, err := t.peopleByTool(ctx)
	if err != nil {
		return nil, err
	}
	out := httpapi.ListAdapters200JSONResponse{Items: []httpapi.Adapter{}}
	for _, a := range t.all() {
		doc, err := t.adapterDoc(a, tools.DisabledAdapters, people)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, doc)
	}
	return out, nil
}

func (t *toolsAPI) UpdateAdapter(ctx context.Context, req httpapi.UpdateAdapterRequestObject) (httpapi.UpdateAdapterResponseObject, error) {
	actor, ok := callerFrom(ctx)
	if !ok {
		return nil, errSignInRequired
	}
	if req.Body == nil {
		return nil, httpapi.NewProblem(http.StatusBadRequest, httpapi.CodeRequestInvalid, "The request has no body.")
	}
	var found adapter.Adapter
	for _, a := range t.all() {
		if a.ID() == req.AdapterId {
			found = a
		}
	}
	if found == nil {
		return nil, errAdapterNotFound
	}
	if err := t.settings.SetAdapterEnabled(ctx, actor, found.ID(), found.Profile().Name, req.Body.Enabled); err != nil {
		return nil, settingsProblem(err)
	}
	disabled, err := t.settings.DisabledAdapters(ctx)
	if err != nil {
		return nil, err
	}
	people, err := t.peopleByTool(ctx)
	if err != nil {
		return nil, err
	}
	doc, err := t.adapterDoc(found, disabled, people)
	if err != nil {
		return nil, err
	}
	return httpapi.UpdateAdapter200JSONResponse(doc), nil
}

// cliVersions are the newest command line this server hands out (its own version) and the
// oldest it supports: the server supports the previous minor version of the command line
// (docs/architecture/08-deployment.md, upgrades), so the floor is MAJOR.(MINOR-1).0, or
// MAJOR.0.0 for a .0 minor. A development build without a version supports everything.
func cliVersions(server string) (latest, floor string) {
	v, ok := settings.ParseVersion(server)
	if !ok {
		return "0.0.0", "0.0.0"
	}
	minor := max(v.Minor-1, 0)
	return v.String(), settings.Version{Major: v.Major, Minor: minor}.String()
}

// cliRange returns the newest command line this server hands out and the oldest it accepts:
// the minimum an administrator set, or the floor when that is lower or unset. The desired
// state tells every computer both.
func (t *toolsAPI) cliRange(ctx context.Context) (latest, minimum string, err error) {
	set, err := t.settings.CLIMinVersion(ctx)
	if err != nil {
		return "", "", err
	}
	p, err := t.policy(ctx, set, false)
	return p.LatestVersion, p.MinVersion, err
}

func (t *toolsAPI) policy(ctx context.Context, set string, usage bool) (httpapi.CLIPolicy, error) {
	latest, floor := cliVersions(t.version)
	p := httpapi.CLIPolicy{LatestVersion: latest, MinVersionFloor: floor, MinVersion: floor, Distribution: []httpapi.VersionCount{}}
	// A minimum set under an older server can be below what this one supports.
	if set != "" && settings.CompareVersions(set, floor) > 0 {
		p.MinVersion = set
	}
	if usage && t.hooks.toolUsage != nil {
		d, err := t.hooks.toolUsage.CLIVersions(ctx)
		if err != nil {
			return httpapi.CLIPolicy{}, err
		}
		if d != nil {
			p.Distribution = d
		}
	}
	return p, nil
}

func (t *toolsAPI) GetCLIPolicy(ctx context.Context, _ httpapi.GetCLIPolicyRequestObject) (httpapi.GetCLIPolicyResponseObject, error) {
	actor, ok := callerFrom(ctx)
	if !ok {
		return nil, errSignInRequired
	}
	tools, err := t.settings.Tools(ctx, actor)
	if err != nil {
		return nil, settingsProblem(err)
	}
	p, err := t.policy(ctx, tools.CLIMinVersion, true)
	if err != nil {
		return nil, err
	}
	return httpapi.GetCLIPolicy200JSONResponse(p), nil
}

func (t *toolsAPI) UpdateCLIPolicy(ctx context.Context, req httpapi.UpdateCLIPolicyRequestObject) (httpapi.UpdateCLIPolicyResponseObject, error) {
	actor, ok := callerFrom(ctx)
	if !ok {
		return nil, errSignInRequired
	}
	if req.Body == nil {
		return nil, httpapi.NewProblem(http.StatusBadRequest, httpapi.CodeRequestInvalid, "The request has no body.")
	}
	_, floor := cliVersions(t.version)
	err := t.settings.SetCLIMinVersion(ctx, actor, req.Body.MinVersion, floor)
	var below *settings.BelowFloorError
	if errors.As(err, &below) {
		detail := "This server needs at least " + below.Floor + "."
		return httpapi.UpdateCLIPolicy409ApplicationProblemPlusJSONResponse{Type: "about:blank", Title: http.StatusText(http.StatusConflict),
			Status: http.StatusConflict, Code: "cli.below_floor", Detail: &detail, Params: &map[string]any{"floor": below.Floor}}, nil
	}
	if err != nil {
		return nil, settingsProblem(err)
	}
	set, err := t.settings.CLIMinVersion(ctx)
	if err != nil {
		return nil, err
	}
	p, err := t.policy(ctx, set, true)
	if err != nil {
		return nil, err
	}
	return httpapi.UpdateCLIPolicy200JSONResponse(p), nil
}

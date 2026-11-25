// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package doctor

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
)

// toolRootsCheck: no configuration directory of an installed tool is reached through a symbolic
// link that leads out of the home directory, as dotfiles managers such as GNU Stow set up. The
// executor writes through an os.Root opened on the home directory, which refuses to follow such
// a link, so every write to that tool would fail. Links that stay inside the home directory are
// fine, and so is a root that the tool's environment variable moves out of the home directory
// (the executor opens that one directly).
type toolRootsCheck struct{}

func (toolRootsCheck) ID() string { return "tool-roots" }

func (toolRootsCheck) Title() string { return msg.DoctorToolRootsTitle }

func (toolRootsCheck) Run(ctx context.Context, env *Env) []Result {
	var out []Result
	for _, d := range env.Detections(ctx) {
		if d.Disabled || d.Err != nil || d.Detection.Status != adapter.Installed {
			continue
		}
		name := d.Adapter.Profile().Name
		if name == "" {
			name = d.Adapter.ID()
		}
		tool := i18n.V("tool", name)
		before := len(out)
		reported := map[string]bool{} // two roots below one link: the link once
		for _, root := range sortedRoots(d.Detection.Roots) {
			if res, bad := checkRoot(env, root, tool); bad && !reported[res.Path] {
				reported[res.Path] = true
				res.Subject = d.Adapter.ID()
				out = append(out, res)
			}
		}
		if len(out) == before {
			out = append(out, Result{Subject: d.Adapter.ID(), Status: OK, Code: "ok",
				Message: M(msg.DoctorToolRootsOk, tool)})
		}
	}
	if len(out) == 0 {
		return []Result{{Status: Skipped, Code: "no_tools", Message: M(msg.DoctorToolRootsNone)}}
	}
	return out
}

// sortedRoots returns the distinct root paths, sorted, so that results come in a stable order.
func sortedRoots(roots map[string]string) []string {
	var out []string
	for _, r := range roots {
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	slices.Sort(out)
	return out
}

// checkRoot walks from the home directory down to root ("~/…") and reports the first element
// that is a symbolic link leading out of the home directory, or that cannot be looked at.
func checkRoot(env *Env, root string, tool i18n.Arg) (Result, bool) {
	rel, ok := strings.CutPrefix(root, "~/")
	if !ok || env.UserHome == "" {
		return Result{}, false // outside the home directory: the executor opens it as a root of its own
	}
	realHome, err := filepath.EvalSymlinks(env.UserHome)
	if err != nil {
		return unreadable(env.UserHome, err, M(msg.DoctorToolRootsUnreadable,
			i18n.V("path", env.show(env.UserHome)), i18n.V("detail", err.Error()))), true
	}
	p := env.UserHome
	for _, part := range strings.Split(rel, "/") {
		if part == "" {
			continue
		}
		p = filepath.Join(p, part)
		fi, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return Result{}, false // nothing further down can be a link yet
		}
		if err != nil {
			return unreadable(p, err, M(msg.DoctorToolRootsUnreadable,
				i18n.V("path", env.show(p)), i18n.V("detail", err.Error()))), true
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			continue
		}
		target, err := filepath.EvalSymlinks(p)
		if errors.Is(err, fs.ErrNotExist) {
			return Result{}, false // a dangling link: the tool has nothing there either
		}
		if err != nil {
			return unreadable(p, err, M(msg.DoctorToolRootsUnreadable,
				i18n.V("path", env.show(p)), i18n.V("detail", err.Error()))), true
		}
		if !inside(realHome, target) {
			shownTarget := i18n.V("target", env.show(target))
			return Result{Status: Fail, Code: "link_outside_home", Path: p,
				Message: M(msg.DoctorToolRootsOutside, i18n.V("path", env.show(p)), shownTarget),
				Hint:    M(msg.DoctorToolRootsOutsideHint, tool, shownTarget),
				Facts:   map[string]string{"target": target}}, true
		}
	}
	return Result{}, false
}

// inside reports whether p lies in dir (both already free of symbolic links).
func inside(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

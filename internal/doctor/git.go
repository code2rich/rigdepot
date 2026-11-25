// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package doctor

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/gitrepo"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
)

// gitCheck: the git program is there and runs. Syncing straight from a resource repository
// calls it; with a server the command line downloads bundles instead and does not need it, so
// a missing git is then only a warning.
type gitCheck struct{}

func (gitCheck) ID() string { return "git" }

func (gitCheck) Title() string { return msg.DoctorGitTitle }

// gitTimeout limits "git version", which answers at once when git works at all.
const gitTimeout = 10 * time.Second

func (gitCheck) Run(ctx context.Context, env *Env) []Result {
	gitEnv := i18n.V("env", brand.EnvPrefix+"GIT")
	// The cache directory is required by gitrepo.New but "git version" never touches it.
	cache := "cache"
	if env.homeKnown() {
		cache = env.Home.Join(home.CacheDir, "repos")
	}
	f, err := gitrepo.New(gitrepo.Options{Git: env.Git, CacheDir: cache, Timeout: gitTimeout})
	if err != nil {
		status := Fail
		if env.Server != "" {
			status = Warn
		}
		res := Result{Status: status, Code: "missing", Message: M(msg.DoctorGitMissing), Hint: M(msg.DoctorGitMissingHint, gitEnv)}
		if env.Git != "" {
			res.Code, res.Path = "not_found", env.Git
			res.Message = M(msg.DoctorGitNotFound, i18n.V("path", env.show(env.Git)))
		}
		if !errors.Is(err, gitrepo.ErrNoGit) {
			res.Status, res.Code = Unknown, "unknown"
		}
		return []Result{res}
	}

	name := env.Git
	if name == "" {
		name = "git"
	}
	path, err := exec.LookPath(name)
	if err != nil {
		path = name
	}
	shown := i18n.V("path", env.show(path))
	out, err := f.Version(ctx)
	if err != nil {
		return []Result{{Status: Fail, Code: "broken", Path: path,
			Message: M(msg.DoctorGitBroken, shown, i18n.V("detail", err.Error())),
			Hint:    M(msg.DoctorGitBrokenHint, shown, gitEnv)}}
	}
	version := strings.TrimSpace(strings.TrimPrefix(out, "git version"))
	return []Result{{Status: OK, Code: "ok", Path: path,
		Message: M(msg.DoctorGitOk, shown, i18n.V("version", out)),
		Facts:   map[string]string{"version": version}}}
}

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/code2rich/rigdepot/internal/gitrepo"
	"github.com/code2rich/rigdepot/internal/server/catalog"
	"github.com/code2rich/rigdepot/internal/server/jobs"
)

// sourceSyncTimeout is how long one run of the source-sync job may take: every source, each
// git command limited by gitrepo's own timeout.
const sourceSyncTimeout = 30 * time.Minute

// addSourceSync adds the job that pulls the resource repositories (catalog.PullSources) and the
// readiness item "sources": a repository that failed its last pull degrades the server, which
// keeps serving what the last good pull imported.
//
// The repositories are kept as bare clones in <data>/cache/repos. The git program is looked up
// on every run, so that installing git later needs no restart; until then every run fails and
// says why.
func (a *App) addSourceSync() error {
	cat := a.catalog
	opts := gitrepo.Options{
		Git:        a.cfg.Git,
		CacheDir:   filepath.Join(a.cfg.DataDir, "cache", "repos"),
		AllowLocal: a.catalogOptions.AllowLocalSources,
	}
	run := func(ctx context.Context) error {
		f, err := gitrepo.New(opts)
		if err != nil {
			return err
		}
		return cat.PullSources(ctx, f)
	}
	if err := a.bg.Jobs.Add(jobs.Job{Name: catalog.SourceSyncJob, Every: catalog.SourcePullEvery, Timeout: sourceSyncTimeout, Run: run}); err != nil {
		return err
	}
	a.AddReadyCheck(ReadyCheck{Name: "sources", Check: func(ctx context.Context) error {
		failing, err := cat.SourcesFailing(ctx)
		if err != nil {
			return err
		}
		if len(failing) > 0 {
			return fmt.Errorf("the last pull failed: %s", strings.Join(failing, ", "))
		}
		return nil
	}})
	return nil
}

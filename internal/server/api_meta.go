// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"sync"

	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/server/config"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
	"github.com/code2rich/rigdepot/internal/server/modhost"
	"github.com/code2rich/rigdepot/internal/server/settings"
	"github.com/code2rich/rigdepot/locales"
)

// metaAPI implements getMeta: what the console needs before anyone signs in. It is public, so it
// says nothing an anonymous visitor should not know: the version, the organization's name, the
// languages, the enabled sign-in methods (their ids and names, never their settings), the
// enabled modules, the command-line versions and the upload limit.
type metaAPI struct {
	cfg      *config.Config
	settings *settings.Service
	// methods are the enabled sign-in methods (authn); nil while there is no authn.
	methods func(ctx context.Context) ([]httpapi.LoginMethod, error)
	// modules is the module host; nil while nothing runs in the background: no modules.
	modules *modhost.Host
	version string
}

// metaLocales are the languages of the catalogs, read once.
var metaLocales = sync.OnceValues(func() ([]string, error) {
	cat, err := i18n.Load(locales.FS, "server")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range cat.Languages() {
		out = append(out, l.Tag.String())
	}
	return out, nil
})

func (m *metaAPI) GetMeta(ctx context.Context, _ httpapi.GetMetaRequestObject) (httpapi.GetMetaResponseObject, error) {
	out := httpapi.GetMeta200JSONResponse{
		Version: m.version, Api: httpapi.V1, DefaultLocale: m.cfg.DefaultLocale,
		LoginMethods: []httpapi.LoginMethod{}, Modules: []string{},
	}
	out.Limits.MaxBundleBytes = m.cfg.MaxBundleMB << 20

	locs, err := metaLocales()
	if err != nil {
		return nil, err
	}
	out.Locales = locs

	setup, err := m.settings.Setup(ctx)
	if err != nil {
		return nil, err
	}
	out.SetupComplete = setup.Complete
	org, err := m.settings.OrganizationName(ctx)
	if err != nil {
		return nil, err
	}
	if org != "" {
		out.Organization = &org
	}

	if m.methods != nil {
		if out.LoginMethods, err = m.methods(ctx); err != nil {
			return nil, err
		}
	}
	if m.modules != nil {
		out.Modules = m.modules.Enabled()
	}

	latest, floor := cliVersions(m.version)
	out.Cli = httpapi.CLIVersions{LatestVersion: latest, MinVersion: floor}
	set, err := m.settings.CLIMinVersion(ctx)
	if err != nil {
		return nil, err
	}
	if set != "" && settings.CompareVersions(set, floor) > 0 {
		out.Cli.MinVersion = set
	}
	return out, nil
}

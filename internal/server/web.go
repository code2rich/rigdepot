// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"net/http"

	"github.com/code2rich/rigdepot/internal/server/webui"
	"github.com/code2rich/rigdepot/web"
)

// webMount builds the handler of the console (Mounts.Web): the files built
// into the program, or, with RGD_WEB_DEV_PROXY, the development server on
// this machine.
func (a *App) webMount() (http.Handler, error) {
	opts := webui.Options{
		Files:         web.FS(),
		BasePath:      a.cfg.BaseURL.Path,
		DefaultLocale: a.cfg.DefaultLocale,
		HSTS:          a.cfg.BaseURL.Scheme == "https",
		DevProxy:      a.cfg.WebDevProxy,
		Log:           a.log,
	}
	switch {
	case opts.DevProxy != nil:
		a.log.Warn("serving the web console from its development server; never do this in production", "web_dev_proxy", opts.DevProxy.String())
	case !web.Built:
		a.log.Info("this program was built without the web console; it serves a page that says how to build it")
	}
	return webui.New(opts)
}

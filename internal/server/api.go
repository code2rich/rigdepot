// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

// apiMounts builds the handlers that serve mounts: the API under /api/ and
// the console everywhere else.
//
// This is where the HTTP layer plugs in. Once internal/server/httpapi
// (generated from api/openapi.yaml, task M1-01) and the domain services
// exist, this function constructs the domain services on app.db and returns
// Mounts{API: httpapi.NewHandler(...)}. The handler receives the full path
// (/api/v1/…) with the base URL's path already removed, and sees a request
// context that carries a logging.Request: calling logging.SetRoute with the
// route template and logging.SetUser with the user's ID puts them on the
// request log line. Domain services that have a readiness item register it
// with app.AddReadyCheck. The console is not built here: serve mounts it
// (webMount, web.go) when Mounts.Web is left nil.
//
// Until then the API is not mounted: /api/ answers 404.
func apiMounts(app *App) Mounts {
	_ = app
	return Mounts{}
}

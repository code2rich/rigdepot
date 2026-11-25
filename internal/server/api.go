// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"net/http"

	"github.com/code2rich/rigdepot/internal/logging"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
	"github.com/code2rich/rigdepot/internal/server/settings"
)

// api is the whole contract, httpapi.StrictServerInterface, assembled from
// one implementation per domain. It is the one place where the domains'
// implementations meet:
//
//   - Each domain writes a struct in this package (api_<domain>.go) with the
//     methods of its operations, holding the domain services it calls, and
//     embeds a pointer to it here.
//   - notImplemented (api_fallback.go) answers every operation with 501. It
//     sits one level deeper (through fallback), so a domain's method always
//     wins over it; nothing has to be removed from it when a domain
//     implements an operation.
//   - Two domains implementing the same operation make the method ambiguous,
//     and the assertion below stops compiling: one operation, one owner.
//
// The structs translate between the contract's types and the domain's and
// map the domain's errors to problem documents (httpapi.ProblemError); the
// rules live in the domain packages.
type api struct {
	fallback
	*settingsAPI
	*setupAPI
	*auditAPI
}

// fallback puts notImplemented's methods one embedding level below the
// domains'.
type fallback struct{ notImplemented }

var _ httpapi.StrictServerInterface = api{}

// hooks are what the API needs from packages other tasks build: each is
// nil until its package exists, and the operations that need it refuse or
// answer 501 meanwhile. The assembly sets them in openServices.
type hooks struct {
	// authorize decides whether actor may do action on object (the zero
	// Object for actions on the platform as a whole). The access package
	// (M1-12) provides it; nil refuses every protected operation.
	authorize func(ctx context.Context, actor audit.Actor, action string, object audit.Object) (bool, error)
	// firstAdmin creates the setup wizard's first administrator and signs
	// them in (access and authn, M1-12 and M1-20). Nil: createSetupAdmin
	// answers 501.
	firstAdmin firstAdminCreator
	// names turns user and device ids into names for the audit log's
	// entries (access and authn). Nil: the ids stand in for the names.
	names auditNames
	// sendTestMail sends a test message with the given mail server
	// (notify, M1-18). Nil: sendTestMail answers 501.
	sendTestMail func(ctx context.Context, server settings.MailConfig, to string) error
}

// apiMounts builds the handlers that serve mounts: the API under /api/ and
// the console everywhere else (the console, internal/server/webui, is task
// M1-15).
//
// The API handler receives the full path (/api/v1/…), with the base URL's
// path already removed, and a request context that carries a
// logging.Request. Signing in (authn, M1-20) goes around it as middleware
// that puts the caller into the context with withCaller.
func apiMounts(app *App) Mounts {
	impl := api{
		settingsAPI: &settingsAPI{settings: app.settings, hooks: &app.hooks},
		setupAPI:    &setupAPI{settings: app.settings, hooks: &app.hooks},
		auditAPI:    &auditAPI{log: app.audit, hooks: &app.hooks},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", notFound) // paths the contract does not have
	h := httpapi.NewHandler(impl, mux)
	return Mounts{API: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		// The mux has set the pattern it matched (GET /api/v1/settings) on
		// r; log that instead of the real path.
		if r.Pattern != "" {
			logging.SetRoute(r.Context(), r.Pattern)
		}
	})}
}

type callerKey struct{}

// withCaller returns ctx carrying who sends the request. The sign-in
// middleware (authn, M1-20) calls it once it has checked the session or
// token; it fills in the origin (web or cli), the device and X-Via-Tool.
func withCaller(ctx context.Context, a audit.Actor) context.Context {
	return context.WithValue(ctx, callerKey{}, a)
}

// callerFrom returns who sends the request; false when nobody is signed in.
func callerFrom(ctx context.Context) (audit.Actor, bool) {
	a, ok := ctx.Value(callerKey{}).(audit.Actor)
	return a, ok && a.UserID != ""
}

// errSignInRequired is the answer to an operation that needs a caller when
// there is none.
var errSignInRequired = httpapi.NewProblem(http.StatusUnauthorized, "auth.required", "Sign in first.")

// errDenied is the answer to an operation the caller may not do.
var errDenied = httpapi.NewProblem(http.StatusForbidden, "access.denied", "You are not allowed to do this.")

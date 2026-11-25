// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"net/http"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/authn"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
	"github.com/code2rich/rigdepot/internal/server/httpapi/accessapi"
	"github.com/code2rich/rigdepot/internal/server/httpapi/authnapi"
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
	*accessapi.Handlers
	*authnapi.Ops
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
	// Object for actions on the platform as a whole). openServices sets it
	// to ask the access package (accessAuthorizer); nil refuses every
	// protected operation.
	authorize func(ctx context.Context, actor audit.Actor, action string, object audit.Object) (bool, error)
	// firstAdmin creates the setup wizard's first administrator and signs
	// them in: openServices sets it to authnapi.Ops. Nil: createSetupAdmin
	// answers 501.
	firstAdmin firstAdminCreator
	// names turns user and device ids into names for the audit log's
	// entries. openServices sets it to read users from access; device
	// names come with the device table (authn, M1-22). Nil: the ids stand
	// in for the names.
	names auditNames
	// devices verifies the command-line program's device tokens (the device
	// code flow, M1-22). Nil: every device token is refused.
	devices authn.DeviceTokens
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
// logging.Request. Signing in (authnapi.Ops.Middleware) goes around it: it
// checks the credential each operation's security list asks for and puts
// the caller into the context with access.WithActor.
func apiMounts(app *App) Mounts {
	impl := api{
		Handlers: accessapi.New(app.access, accessapi.Options{
			BaseURL: app.cfg.BaseURL,
			SignIns: authnapi.SignIns(app.authn),
		}),
		Ops:         app.authnOps,
		settingsAPI: &settingsAPI{settings: app.settings, hooks: &app.hooks},
		setupAPI:    &setupAPI{settings: app.settings, hooks: &app.hooks},
		auditAPI:    &auditAPI{log: app.audit, hooks: &app.hooks},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", notFound) // paths the contract does not have
	// The middleware logs the pattern of the operation (GET
	// /api/v1/settings) instead of the real path.
	return Mounts{API: app.authnOps.Middleware(httpapi.NewHandler(impl, mux))}
}

// callerFrom returns who sends the request, as the audit log records it;
// false when nobody is signed in. The sign-in middleware
// (authnapi.Ops.Middleware) puts the caller into the context with
// access.WithActor once it has checked the session or token, with the
// origin (web or cli), the device and X-Via-Tool. The server itself (access.System) never calls the API.
func callerFrom(ctx context.Context) (audit.Actor, bool) {
	a, ok := access.ActorFrom(ctx)
	if !ok || a.UserID == "" {
		return audit.Actor{}, false
	}
	return auditActor(a), true
}

// errSignInRequired is the answer to an operation that needs a caller when
// there is none.
var errSignInRequired = httpapi.NewProblem(http.StatusUnauthorized, "auth.required", "Sign in first.")

// errDenied is the answer to an operation the caller may not do.
var errDenied = httpapi.NewProblem(http.StatusForbidden, "access.denied", "You are not allowed to do this.")

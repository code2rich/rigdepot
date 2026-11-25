// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/code2rich/rigdepot/internal/buildinfo"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/authn"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
	"github.com/code2rich/rigdepot/internal/server/httpapi/accessapi"
	"github.com/code2rich/rigdepot/internal/server/httpapi/authnapi"
	"github.com/code2rich/rigdepot/internal/server/modhost"
	"github.com/code2rich/rigdepot/internal/server/settings"
	"github.com/code2rich/rigdepot/locales"
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
	*modulesAPI
	*toolsAPI
	*catalogAPI
	*metaAPI
	*notificationsAPI
	*cliAPI
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
	// entries. openServices sets it to read users from access and devices
	// from authn. Nil: the ids stand in for the names.
	names auditNames
	// devices verifies the command-line program's device tokens.
	// openServices sets it to the device code flow (authn.Devices); tests
	// put a stand-in here.
	devices authn.DeviceTokens
	// sendTestMail sends a test message with the given mail server
	// (notify, M1-18). Nil: sendTestMail answers 501.
	sendTestMail func(ctx context.Context, server settings.MailConfig, to string) error
	// toolUsage counts people by tool and by command-line version, from
	// the devices (authn, M1-20) and the install reports (delivery). Nil:
	// the tool adapters page shows no counts (0 people, an empty
	// distribution).
	toolUsage toolUsage
	// delivery tells the catalog pages what people turned on or off and where resources are
	// installed (delivery, M1-27). Nil: everything is on by its policy and installed nowhere.
	delivery catalogDelivery
	// secretsConfigured returns, by secret name, which secrets a person has set (secrets,
	// M1-28). Nil: none.
	secretsConfigured func(ctx context.Context, userID string) (map[string]bool, error)
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
	texts, err := i18n.Load(locales.FS, "check")
	if err != nil {
		panic(err) // the catalogues are embedded and checked by tools/dev check
	}
	accessOpts := accessapi.Options{
		BaseURL:        app.cfg.BaseURL,
		SignIns:        authnapi.SignIns(app.authn),
		ResourceCounts: app.catalog.ResourceCounts,
	}
	if bg := app.bg; bg != nil && bg.Notify != nil {
		// Invitations go out by mail once mail is set up; until then the
		// administrator gets the link (notify.ErrMailOff).
		accessOpts.MailInvite = bg.Notify.MailInvite
	}
	impl := api{
		Handlers:         accessapi.New(app.access, accessOpts),
		Ops:              app.authnOps,
		settingsAPI:      &settingsAPI{settings: app.settings, hooks: &app.hooks},
		setupAPI:         &setupAPI{settings: app.settings, hooks: &app.hooks},
		auditAPI:         &auditAPI{log: app.audit, hooks: &app.hooks},
		modulesAPI:       &modulesAPI{settings: app.settings},
		toolsAPI:         &toolsAPI{settings: app.settings, hooks: &app.hooks, adapters: app.adapters, version: app.version},
		catalogAPI:       &catalogAPI{cat: app.catalog, hooks: &app.hooks, texts: texts, defaultLocale: app.cfg.DefaultLocale},
		notificationsAPI: &notificationsAPI{access: app.access},
		cliAPI:           &cliAPI{dist: app.cli, base: publicBase(app.cfg.BaseURL), downloadBase: app.cfg.CLIDownloadBase, version: app.version, log: app.log},
	}
	if impl.toolsAPI.version == "" {
		impl.toolsAPI.version = buildinfo.Get().Version
	}
	if impl.cliAPI.version == "" {
		impl.cliAPI.version = buildinfo.Get().Version
	}
	impl.metaAPI = &metaAPI{cfg: app.cfg, settings: app.settings, version: impl.toolsAPI.version}
	if app.authnOps != nil {
		impl.metaAPI.methods = app.authnOps.LoginMethods
	}
	// The routes of the optional modules that are on, under /api/v1/x/<id>/;
	// they log their own route templates.
	var modules http.Handler
	if bg := app.bg; bg != nil && bg.Modules != nil {
		impl.modulesAPI.host = bg.Modules
		impl.metaAPI.modules = bg.Modules
		impl.modulesAPI.text = bg.text
		modules = bg.Modules.Handler(http.HandlerFunc(notFound))
	}
	if bg := app.bg; bg != nil && bg.Notify != nil {
		impl.notificationsAPI.notify = bg.Notify
	}
	uploadLimit := int64(app.cfg.MaxBundleMB) << 20
	mux := http.NewServeMux()
	mux.HandleFunc("/", notFound) // paths the contract does not have
	h := httpapi.NewHandler(impl, mux)
	// Module routes sit behind the same sign-in middleware as the contract's
	// operations. The middleware logs the pattern of the operation (GET
	// /api/v1/settings) instead of the real path.
	return Mounts{API: app.authnOps.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if modules != nil && strings.HasPrefix(r.URL.Path, modhost.RoutePrefix) {
			modules.ServeHTTP(w, r)
			return
		}
		// An upload over the limit is refused before its body is read.
		if !limitUpload(w, r, uploadLimit) {
			return
		}
		h.ServeHTTP(w, withAcceptLanguage(r))
	}))}
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

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

// This file is the code layout as data: which group each package belongs to
// and which third-party modules built-in implementations may use. The
// dependency rules in deprules.go are checked against it. A new package under
// internal/ has to be added here before check passes, which keeps the map of
// the repository honest.

// sharedPackages are the internal packages both programs use.
var sharedPackages = []string{
	"internal/brand",
	"internal/buildinfo",
	"internal/i18n",
	"internal/logging",
	"internal/repoformat",
	"internal/bundle",
	"internal/check",
	"internal/gitrepo",
	"internal/testenv",
}

// cliPackages are the internal packages only the command-line program uses.
var cliPackages = []string{
	"internal/cli",
	"internal/cliout",
	"internal/localconfig",
	"internal/home",
	"internal/manifest",
	"internal/configedit",
	"internal/executor",
	"internal/sandbox",
	"internal/desired",
	"internal/sync",
	"internal/flow",
	"internal/apiclient",
	"internal/keyring",
	"internal/device",
	"internal/localsecrets",
	"internal/mcprun",
	"internal/doctor",
}

// serverRoot is the server side: this package and everything below it.
const serverRoot = "internal/server"

// serverDomainEdges lists, for each domain package of the server, the other
// domain packages it may import (rule R6). notify is deliberately imported by
// nobody: code that publishes a resource emits an event and does not know
// that a notification follows.
var serverDomainEdges = map[string][]string{
	"access":   {"audit", "events"},
	"authn":    {"access", "audit", "events"},
	"catalog":  {"access", "audit", "events"},
	"delivery": {"catalog", "access", "secrets", "audit"},
	"secrets":  {"access", "audit"},
	"feedback": {"access", "catalog", "audit", "events"},
	"notify":   {"access", "events"},
	"settings": {"audit"},
	"audit":    {},
	"events":   {},
}

// serverFoundation are the server packages that the domain packages build
// on. They must not import a domain package, the HTTP layer or the module
// host.
var serverFoundation = []string{"config", "dbx", "migrations", "masterkey", "jobs"}

// serverUpper are the server packages above the domain packages; only the
// server root may import them.
var serverUpper = []string{"httpapi", "modhost", "webui"}

// allowedThirdParty are the module paths built-in implementations may import
// besides the standard library and ext/ (rule R2). Adding a dependency needs
// a maintainer's approval first; see CONTRIBUTING.md.
var allowedThirdParty = []string{
	"go.yaml.in/yaml/v4",
	"github.com/santhosh-tekuri/jsonschema/v6",
	"github.com/pelletier/go-toml/v2",
	"github.com/tailscale/hujson",
	"golang.org/x/sys",
	"golang.org/x/text",
	"golang.org/x/crypto",
	"golang.org/x/oauth2",
	"github.com/coreos/go-oidc/v3",
}

// yamlModule is the one third-party module ext/adapter/profileyaml may use
// (the second exception of rule R1).
const yamlModule = "go.yaml.in/yaml/v4"

const profileYAMLPackage = "ext/adapter/profileyaml"

// forbiddenNames are package names that say nothing about what the package
// provides (rule R8).
var forbiddenNames = []string{"util", "utils", "common", "helper", "helpers", "misc", "base"}

// cliForbidden are import path fragments that must never appear in the
// dependency closure of the command-line program: no database driver, no
// identity protocol, nothing from the server side (rule R5).
var cliForbidden = []string{
	"modernc.org/sqlite",
	"github.com/jackc/pgx",
	"github.com/coreos/go-oidc",
}

// programForbidden are standard packages that must not be linked into either program: test
// code has no place in what is shipped.
var programForbidden = []string{"testing"}

// cliForbiddenLocal are the packages of this module that must not appear in
// the dependency closure of the command-line program.
var cliForbiddenLocal = []string{serverRoot, "builtin/identity", "builtin/notifiers"}

// serverForbiddenLocal are the packages of this module that must not appear
// in the dependency closure of the server: it has no business editing other
// tools' configuration.
var serverForbiddenLocal = []string{
	"internal/sync",
	"internal/executor",
	"internal/sandbox",
	"internal/keyring",
	"internal/configedit",
}

// registries maps each class of built-in implementation to the registry
// package where it has to be registered, the file to edit and the line to
// add (with %s standing for the package name).
var registries = map[string]struct{ pkg, file, line string }{
	"builtin/adapters":  {"builtin/all", "adapters.go", "adapter.Register(%s.New())"},
	"builtin/kinds":     {"builtin/all", "kinds.go", "kind.Register(%s.New())"},
	"builtin/checks":    {"builtin/all", "checks.go", "check.Register(%s.New())"},
	"builtin/identity":  {"builtin/all/server", "identity.go", "identity.Register(%s.New())"},
	"builtin/notifiers": {"builtin/all/server", "notifiers.go", "notify.Register(%s.New())"},
	"modules":           {"modules/all", "maybe_%s.go", "module.Register(%s.New())"},
}

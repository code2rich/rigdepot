// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"strings"
	"testing"
)

const testModule = "example.test/depot"

// local turns paths relative to the test module into full import paths.
func local(paths ...string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = testModule + "/" + p
	}
	return out
}

func TestDependencyRules(t *testing.T) {
	setClosureEntries(brandNames{CLI: "depot", Server: "depot-server"})

	tests := []struct {
		name string
		pkgs []pkg
		// want is "RULE: from -> to" for each expected violation; empty means
		// the graph is allowed.
		want []string
	}{
		{
			name: "the intended shape is allowed",
			pkgs: []pkg{
				{Path: "cmd/depot", Imports: local("builtin/all", "internal/cli")},
				{Path: "cmd/depot-server", Imports: local("builtin/all", "builtin/all/server", "modules/all", "internal/server")},
				{Path: "ext/adapter", Imports: append(local("ext/resource"), "context", "io/fs"), TestImports: local("internal/testenv")},
				{Path: "ext/adapter/adaptertest", Imports: local("ext/adapter", "internal/testenv", "internal/executor", "internal/sandbox")},
				{Path: "ext/adapter/profileyaml", Imports: append(local("ext/adapter"), "go.yaml.in/yaml/v4")},
				{Path: "builtin/adapters/codex", Imports: append(local("ext/adapter", "ext/adapter/profileyaml", "builtin/adapters/internal/paths"), "github.com/pelletier/go-toml/v2"), TestImports: local("ext/adapter/adaptertest")},
				{Path: "builtin/adapters/codex/internal/render", Imports: local("ext/adapter")},
				{Path: "builtin/all", Imports: local("builtin/adapters/codex", "ext/adapter")},
				{Path: "builtin/all/server", Imports: local("builtin/identity/oidc", "ext/identity")},
				{Path: "modules/insights", Imports: local("ext/module", "modules/insights/store")},
				{Path: "modules/all", Imports: local("modules/insights", "ext/module")},
				{Path: "internal/cli", Imports: local("internal/sync", "internal/brand", "ext/adapter")},
				{Path: "internal/sync", Imports: local("internal/executor", "internal/repoformat")},
				{Path: "internal/server", Imports: local("internal/server/httpapi", "internal/server/modhost", "internal/server/catalog", "internal/server/dbx")},
				{Path: "internal/server/httpapi", Imports: local("internal/server/catalog", "internal/server/notify")},
				{Path: "internal/server/catalog", Imports: local("internal/server/access", "internal/server/audit", "internal/server/events", "internal/server/dbx", "internal/gitrepo")},
				{Path: "internal/server/delivery", Imports: local("internal/server/catalog", "internal/server/secrets")},
				{Path: "internal/server/secrets", Imports: local("internal/server/masterkey", "internal/server/access")},
				{Path: "tools/dev", Imports: []string{"os", "go/ast"}, TestImports: local("internal/testenv")},
			},
		},
		{
			name: "R1: a public interface imports the core",
			pkgs: []pkg{{Path: "ext/adapter", Imports: local("internal/executor")}},
			want: []string{"R1: ext/adapter -> internal/executor"},
		},
		{
			name: "R1: a public interface imports a third-party module",
			pkgs: []pkg{{Path: "ext/kind", Imports: []string{"go.yaml.in/yaml/v4"}}},
			want: []string{"R1: ext/kind -> go.yaml.in/yaml/v4"},
		},
		{
			name: "R1: a public interface pulls in a test suite",
			pkgs: []pkg{{Path: "ext/adapter", Imports: local("ext/adapter/adaptertest")}},
			want: []string{"R1: ext/adapter -> ext/adapter/adaptertest"},
		},
		{
			name: "R1: the YAML parser may not use another third-party module",
			pkgs: []pkg{{Path: "ext/adapter/profileyaml", Imports: []string{"github.com/pelletier/go-toml/v2"}}},
			want: []string{"R1: ext/adapter/profileyaml -> github.com/pelletier/go-toml/v2"},
		},
		{
			name: "R2: a built-in implementation imports the core",
			pkgs: []pkg{{Path: "builtin/checks/secrets", Imports: local("internal/check")}},
			want: []string{"R2: builtin/checks/secrets -> internal/check"},
		},
		{
			name: "R2: a built-in implementation imports the core from a test",
			pkgs: []pkg{{Path: "builtin/checks/secrets", TestImports: local("internal/testenv")}},
			want: []string{"R2: builtin/checks/secrets -> internal/testenv"},
		},
		{
			name: "R2: a registry imports the core",
			pkgs: []pkg{{Path: "builtin/all", Imports: local("internal/brand")}},
			want: []string{"R2: builtin/all -> internal/brand"},
		},
		{
			name: "R2: one built-in item imports another",
			pkgs: []pkg{{Path: "builtin/identity/gitlab", Imports: local("builtin/identity/oidc")}},
			want: []string{"R2: builtin/identity/gitlab -> builtin/identity/oidc"},
		},
		{
			name: "R2: a third-party module that is not on the list",
			pkgs: []pkg{{Path: "builtin/notifiers/email", Imports: []string{"github.com/example/smtp"}}},
			want: []string{"R2: builtin/notifiers/email -> github.com/example/smtp"},
		},
		{
			name: "R3: the core imports a built-in item and a module",
			pkgs: []pkg{{Path: "internal/sync", Imports: local("builtin/adapters/codex", "modules/insights")}},
			want: []string{"R3: internal/sync -> builtin/adapters/codex", "R3: internal/sync -> modules/insights"},
		},
		{
			name: "R4: a module imports the core and another module",
			pkgs: []pkg{{Path: "modules/insights", Imports: local("internal/server/dbx", "modules/review")}},
			want: []string{"R4: modules/insights -> internal/server/dbx", "R4: modules/insights -> modules/review"},
		},
		{
			name: "R5: the command-line side imports the server",
			pkgs: []pkg{{Path: "internal/sync", Imports: local("internal/server/catalog")}},
			want: []string{"R5: internal/sync -> internal/server/catalog"},
		},
		{
			name: "R5: the server imports the command-line side",
			pkgs: []pkg{{Path: "internal/server/delivery", Imports: local("internal/executor")}},
			want: []string{"R5: internal/server/delivery -> internal/executor"},
		},
		{
			name: "R5: a shared package imports the command-line side",
			pkgs: []pkg{{Path: "internal/check", Imports: local("internal/manifest")}},
			want: []string{"R5: internal/check -> internal/manifest"},
		},
		{
			name: "R5: a database driver reaches the command-line program through a shared package",
			pkgs: []pkg{
				{Path: "cmd/depot", Imports: local("internal/cli")},
				{Path: "internal/cli", Imports: local("internal/repoformat")},
				{Path: "internal/repoformat", Imports: []string{"modernc.org/sqlite"}},
			},
			want: []string{"R5: cmd/depot -> modernc.org/sqlite"},
		},
		{
			name: "R5: an identity source reaches the command-line program through the registry",
			pkgs: []pkg{
				{Path: "cmd/depot", Imports: local("builtin/all")},
				{Path: "builtin/all", Imports: local("builtin/identity/oidc")},
				{Path: "builtin/identity/oidc"},
			},
			want: []string{"R5: cmd/depot -> builtin/identity/oidc"},
		},
		{
			name: "R5: configuration editing reaches the server",
			pkgs: []pkg{
				{Path: "cmd/depot-server", Imports: local("internal/server")},
				{Path: "internal/server", Imports: local("internal/repoformat")},
				{Path: "internal/repoformat", Imports: local("internal/configedit")},
				{Path: "internal/configedit"},
			},
			want: []string{"R5: cmd/depot-server -> internal/configedit", "R5: internal/repoformat -> internal/configedit"},
		},
		{
			name: "R6: a domain package imports the HTTP layer",
			pkgs: []pkg{{Path: "internal/server/catalog", Imports: local("internal/server/httpapi")}},
			want: []string{"R6: internal/server/catalog -> internal/server/httpapi"},
		},
		{
			name: "R6: domain packages against the allowed direction",
			pkgs: []pkg{
				{Path: "internal/server/access", Imports: local("internal/server/catalog")},
				{Path: "internal/server/catalog", Imports: local("internal/server/notify")},
			},
			want: []string{"R6: internal/server/access -> internal/server/catalog", "R6: internal/server/catalog -> internal/server/notify"},
		},
		{
			name: "R6: the storage layer imports a domain package",
			pkgs: []pkg{{Path: "internal/server/dbx", Imports: local("internal/server/audit")}},
			want: []string{"R6: internal/server/dbx -> internal/server/audit"},
		},
		{
			name: "R8: a package named after nothing",
			pkgs: []pkg{{Path: "internal/server/util", Name: "util"}, {Path: "builtin/checks/internal/common", Name: "common"}},
			want: []string{"R8: builtin/checks/internal/common -> builtin/checks/internal/common", "R8: internal/server/util -> internal/server/util"},
		},
		{
			name: "layout: an internal package nobody has placed",
			pkgs: []pkg{{Path: "internal/newthing", Name: "newthing"}},
			want: []string{"layout: internal/newthing -> internal/newthing"},
		},
		{
			name: "tools: the development entry point uses a third-party module",
			pkgs: []pkg{{Path: "tools/dev", Imports: append(local("internal/brand"), "github.com/spf13/cobra")}},
			want: []string{"tools: tools/dev -> github.com/spf13/cobra", "tools: tools/dev -> internal/brand"},
		},
		{
			name: "cgo",
			pkgs: []pkg{{Path: "internal/keyring", Imports: []string{"C"}}},
			want: []string{"cgo: internal/keyring -> C"},
		},
		{
			name: "no-testing: a program links the testing package",
			pkgs: []pkg{
				{Path: "cmd/depot", Imports: local("internal/keyring")},
				{Path: "internal/keyring", Imports: []string{"os", "testing"}},
				{Path: "cmd/depot-server", Imports: local("internal/server")},
				{Path: "internal/server", Imports: []string{"testing/fstest"}},
				// Tests may import it.
				{Path: "internal/doctor", Imports: []string{"os"}, TestImports: []string{"testing"}},
			},
			want: []string{
				"no-testing: cmd/depot -> testing",
				"no-testing: cmd/depot-server -> testing/fstest",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, v := range checkDeps(testModule, tt.pkgs) {
				if v.Why == "" {
					t.Errorf("%s -> %s has no explanation", v.From, v.To)
				}
				got = append(got, v.Rule+": "+v.From+" -> "+v.To)
			}
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("violations:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(tt.want, "\n  "))
			}
		})
	}
}

func TestClosureViolationShowsTheImportChain(t *testing.T) {
	setClosureEntries(brandNames{CLI: "depot", Server: "depot-server"})
	violations := checkDeps(testModule, []pkg{
		{Path: "cmd/depot", Imports: local("internal/cli")},
		{Path: "internal/cli", Imports: local("internal/repoformat")},
		{Path: "internal/repoformat", Imports: []string{"github.com/jackc/pgx/v5/stdlib"}},
	})
	if len(violations) != 1 {
		t.Fatalf("got %d violations, want 1: %v", len(violations), violations)
	}
	const chain = "cmd/depot -> internal/cli -> internal/repoformat -> github.com/jackc/pgx/v5/stdlib"
	if !strings.Contains(violations[0].Why, chain) {
		t.Errorf("explanation = %q, want it to contain the chain %q", violations[0].Why, chain)
	}
}

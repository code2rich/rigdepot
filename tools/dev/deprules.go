// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path"
	"slices"
	"sort"
	"strings"
)

// pkg is one package of this module as the dependency rules see it. Paths are
// relative to the module ("internal/cli"); imports are full import paths.
type pkg struct {
	Path        string
	Name        string
	Imports     []string // imports of non-test files
	TestImports []string // imports of test files, internal and external
}

// violation is one broken rule: which rule, which package, and what it
// imports that it must not.
type violation struct {
	Rule string
	From string
	To   string
	Why  string
}

func (v violation) String() string {
	return fmt.Sprintf("%s: %s -> %s: %s", v.Rule, v.From, v.To, v.Why)
}

// listPackages asks the go command for every package of the module.
func listPackages(s *sandbox, module string) ([]pkg, error) {
	cmd := exec.Command("go", "list", "-json=ImportPath,Name,Imports,TestImports,XTestImports", "./...")
	cmd.Dir = s.root
	cmd.Env = s.env
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("go list: %w\n%s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("go list: %w", err)
	}
	var pkgs []pkg
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for {
		var raw struct {
			ImportPath   string
			Name         string
			Imports      []string
			TestImports  []string
			XTestImports []string
		}
		if err := dec.Decode(&raw); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("go list: %w", err)
		}
		rel, ok := relPath(module, raw.ImportPath)
		if !ok {
			continue
		}
		pkgs = append(pkgs, pkg{
			Path:        rel,
			Name:        raw.Name,
			Imports:     raw.Imports,
			TestImports: append(raw.TestImports, raw.XTestImports...),
		})
	}
	return pkgs, nil
}

// relPath turns a full import path into a path relative to the module. The
// module root itself is ".".
func relPath(module, importPath string) (string, bool) {
	if importPath == module {
		return ".", true
	}
	if rest, ok := strings.CutPrefix(importPath, module+"/"); ok {
		return rest, true
	}
	return "", false
}

// under reports whether p is dir or a package below it.
func under(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// isStd reports whether an import path belongs to the standard library: its
// first element has no dot.
func isStd(importPath string) bool {
	first, _, _ := strings.Cut(importPath, "/")
	return !strings.Contains(first, ".")
}

func hasModulePrefix(importPath string, modules []string) bool {
	for _, m := range modules {
		if under(importPath, m) {
			return true
		}
	}
	return false
}

// isSuite reports whether an ext/ package is a test suite (adaptertest,
// checktest, kindtest): the first exception of rule R1.
func isSuite(p string) bool {
	return under(p, "ext") && strings.HasSuffix(path.Base(p), "test")
}

// builtinItem returns the directory of the built-in item a package belongs
// to ("builtin/checks/secrets"), or "" for the registries and anything that
// is not an item.
func builtinItem(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) < 3 || parts[0] != "builtin" || parts[1] == "all" {
		return ""
	}
	return strings.Join(parts[:3], "/")
}

// serverLayer returns the first path element below internal/server, or "" for
// the server root and for packages outside the server.
func serverLayer(p string) string {
	rest, ok := strings.CutPrefix(p, serverRoot+"/")
	if !ok {
		return ""
	}
	first, _, _ := strings.Cut(rest, "/")
	return first
}

// checkDeps checks the dependency rules R1 to R6 and R8 against the import
// graph. Rule R7 needs the source text and lives in structure.go.
func checkDeps(module string, pkgs []pkg) []violation {
	var out []violation
	add := func(rule, from, to, why string) {
		out = append(out, violation{Rule: rule, From: from, To: to, Why: why})
	}
	for _, p := range pkgs {
		// R8 looks at the package itself.
		if slices.Contains(forbiddenNames, path.Base(p.Path)) || slices.Contains(forbiddenNames, p.Name) {
			add("R8", p.Path, p.Path, "name the package after what it provides; util, common, helpers, misc and base are not allowed")
		}
		if under(p.Path, "internal") && !underAny(p.Path, sharedPackages) &&
			!underAny(p.Path, cliPackages) && !under(p.Path, serverRoot) {
			add("layout", p.Path, p.Path, "new internal package: add it to sharedPackages or cliPackages in tools/dev/layout.go, or put it under "+serverRoot)
		}
		for _, test := range []bool{false, true} {
			imports := p.Imports
			if test {
				imports = p.TestImports
			}
			for _, imp := range imports {
				if imp == "C" {
					add("cgo", p.Path, imp, "cgo is not used; every target is built with CGO_ENABLED=0")
					continue
				}
				to, local := relPath(module, imp)
				if !local {
					to = imp
				}
				if rule, why := breaks(p.Path, to, local, test); rule != "" {
					add(rule, p.Path, to, why)
				}
			}
		}
	}
	out = append(out, checkClosures(module, pkgs)...)
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return slices.Compact(out)
}

// breaks decides whether package from may import to. local says whether to is
// a package of this module (then it is a relative path). test says whether
// the import comes from a test file.
func breaks(from, to string, local, test bool) (rule, why string) {
	std := !local && isStd(to)
	testenv := local && to == "internal/testenv"

	switch {
	case under(from, "ext"):
		suite := isSuite(from)
		switch {
		case std:
		case local && under(to, "ext"):
			// The two exceptions must not leak into the plain interface
			// packages, or everyone who imports those would get the extra
			// dependencies as well.
			if !suite && !test && isSuite(to) {
				return "R1", "a public interface package must not import a test suite, which depends on the core"
			}
			if !suite && !test && to == profileYAMLPackage && !under(from, profileYAMLPackage) {
				return "R1", "a public interface package must not import the YAML profile parser, which depends on a YAML library"
			}
		case suite && local && under(to, "internal"):
		case test && testenv:
		case under(from, profileYAMLPackage) && !local && under(to, yamlModule):
		default:
			return "R1", "ext/ may only import the standard library and ext/ (exceptions: the *test suites and " + profileYAMLPackage + ")"
		}

	case under(from, "builtin"):
		item := builtinItem(from)
		switch {
		case std, local && under(to, "ext"):
		case !local && hasModulePrefix(to, allowedThirdParty):
		case local && under(to, "internal"):
			return "R2", "built-in implementations may only use ext/; if something is missing there, the public interface needs it"
		case local && under(to, "builtin") && item == "":
			// The registries import every item.
		case local && under(to, "builtin") && (under(to, item) || under(to, path.Dir(item)+"/internal")):
		case local && under(to, "builtin"):
			return "R2", "a built-in item must not import another one; share code through " + path.Dir(item) + "/internal"
		case !local:
			return "R2", "this module is not on the list built-in implementations may use (allowedThirdParty in tools/dev/layout.go)"
		default:
			return "R2", "built-in implementations may only import the standard library, ext/ and the allowed third-party modules"
		}

	case under(from, "modules"):
		parts := strings.Split(from, "/")
		own := strings.Join(parts[:min(2, len(parts))], "/")
		switch {
		case std, local && under(to, "ext"):
		case local && under(to, own):
		case local && own == "modules/all" && under(to, "modules"):
		default:
			return "R4", "a module may only import the standard library, ext/ and its own directory; everything else comes from the module host"
		}

	case under(from, "tools"):
		switch {
		case std, test && testenv:
		default:
			return "tools", "tools/dev uses the standard library only, so that go run ./tools/dev works before anything is downloaded or generated"
		}

	case under(from, "internal"):
		switch {
		case local && (under(to, "builtin") || under(to, "modules")):
			return "R3", "the core only knows interfaces and registries; it must compile without any built-in item or module"
		case local && under(to, serverRoot) && !under(from, serverRoot):
			return "R5", "only the server may import " + serverRoot
		case local && underAny(to, cliPackages) && !underAny(from, cliPackages):
			return "R5", "only the command-line side may import this package"
		case under(from, serverRoot) && local && under(to, serverRoot):
			return breaksServerLayers(from, to)
		}
	}
	return "", ""
}

// breaksServerLayers applies rule R6 to an import between two server
// packages: httpapi -> domain packages -> dbx, and between domain packages
// only along the edges in serverDomainEdges.
func breaksServerLayers(from, to string) (rule, why string) {
	fromLayer, toLayer := serverLayer(from), serverLayer(to)
	if fromLayer == toLayer || fromLayer == "" {
		return "", "" // inside one package tree, or the root that assembles everything
	}
	if toLayer == "" {
		return "R6", "nothing below the server root may import the root that assembles it"
	}
	if slices.Contains(serverUpper, toLayer) {
		return "R6", "only the server root may import " + toLayer + "; domain logic must be testable without it"
	}
	fromEdges, fromDomain := serverDomainEdges[fromLayer]
	_, toDomain := serverDomainEdges[toLayer]
	if slices.Contains(serverFoundation, fromLayer) && (toDomain || slices.Contains(serverUpper, toLayer)) {
		return "R6", fromLayer + " is below the domain packages and must not import one"
	}
	if fromDomain && toDomain && !slices.Contains(fromEdges, toLayer) {
		return "R6", fromLayer + " may only import these domain packages: " + strings.Join(fromEdges, ", ")
	}
	return "", ""
}

// checkClosures checks what must never be linked into each program (the
// transitive half of rule R5). The entry points are found by the programs'
// names in brand.json, passed in through closureEntries.
func checkClosures(module string, pkgs []pkg) []violation {
	byPath := make(map[string]pkg, len(pkgs))
	for _, p := range pkgs {
		byPath[p.Path] = p
	}
	var out []violation
	for _, entry := range closureEntries {
		start, ok := byPath[entry.pkg]
		if !ok {
			continue
		}
		// Breadth-first over this module's packages, remembering how each one
		// was reached so the report can show the chain.
		parent := map[string]string{start.Path: ""}
		queue := []string{start.Path}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			chain := func(last string) string {
				var hops []string
				for at := cur; at != ""; at = parent[at] {
					hops = append([]string{at}, hops...)
				}
				if last != "" {
					hops = append(hops, last)
				}
				return strings.Join(hops, " -> ")
			}
			if cur != start.Path && underAny(cur, entry.local) {
				out = append(out, violation{"R5", start.Path, cur, entry.why + " (import chain: " + chain("") + ")"})
				continue
			}
			for _, imp := range byPath[cur].Imports {
				rel, local := relPath(module, imp)
				if !local {
					if hasModulePrefix(imp, entry.external) {
						out = append(out, violation{"R5", start.Path, imp, entry.why + " (import chain: " + chain(imp) + ")"})
					}
					continue
				}
				if _, seen := parent[rel]; !seen {
					parent[rel] = cur
					queue = append(queue, rel)
				}
			}
		}
	}
	return out
}

func underAny(p string, dirs []string) bool {
	for _, d := range dirs {
		if under(p, d) {
			return true
		}
	}
	return false
}

// closureEntry describes one program and what must stay out of it.
type closureEntry struct {
	pkg      string
	local    []string
	external []string
	why      string
}

// closureEntries is filled in from brand.json before the rules run.
var closureEntries []closureEntry

func setClosureEntries(b brandNames) {
	closureEntries = []closureEntry{
		{
			pkg:      "cmd/" + b.CLI,
			local:    cliForbiddenLocal,
			external: cliForbidden,
			why:      "the command-line program must not link database drivers, identity protocols or anything from the server side",
		},
		{
			pkg:   "cmd/" + b.Server,
			local: serverForbiddenLocal,
			why:   "the server must not link the code that edits other tools' configuration",
		},
	}
}

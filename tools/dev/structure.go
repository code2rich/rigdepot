// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// The checks in this file read the source tree directly. Each returns
// violations in the same shape as the dependency rules.

// skippedDirs are never part of the source: build output, local state,
// dependencies.
var skippedDirs = []string{".git", ".dev", "dist", "node_modules"}

// sourceFile is one file of the repository with its path relative to the
// root, always with forward slashes.
type sourceFile struct {
	rel  string
	data []byte
}

// walkSource returns every regular file of the repository except build
// output and dependencies. Fixtures under testdata are included; the checks
// that must not look at them skip them.
func walkSource(root string) ([]sourceFile, error) {
	var files []sourceFile
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && slices.Contains(skippedDirs, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		files = append(files, sourceFile{rel: filepath.ToSlash(rel), data: data})
		return nil
	})
	return files, err
}

func inTestdata(rel string) bool {
	return strings.HasPrefix(rel, "testdata/") || strings.Contains(rel, "/testdata/")
}

// isGenerated reports whether a file carries the standard marker of
// generated Go code.
func isGenerated(data []byte) bool {
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	return bytes.Contains(head, []byte("// Code generated ")) && bytes.Contains(head, []byte("DO NOT EDIT."))
}

// goFile is a parsed Go file.
type goFile struct {
	rel  string
	dir  string
	test bool
	ast  *ast.File
	fset *token.FileSet
}

func (f goFile) position(n ast.Node) string {
	return fmt.Sprintf("%s:%d", f.rel, f.fset.Position(n.Pos()).Line)
}

// importNames returns the names under which a file refers to the import
// paths that match; a file can import one package under several names.
func (f goFile) importNames(match func(importPath string) bool) []string {
	var names []string
	for _, spec := range f.ast.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil || !match(p) {
			continue
		}
		if spec.Name != nil {
			names = append(names, spec.Name.Name)
		} else {
			names = append(names, path.Base(p))
		}
	}
	return names
}

// parseGo parses the Go files outside testdata. Generated files are left
// out: they are checked by regenerating them.
func parseGo(files []sourceFile) ([]goFile, error) {
	var out []goFile
	for _, f := range files {
		if !strings.HasSuffix(f.rel, ".go") || inTestdata(f.rel) || isGenerated(f.data) {
			continue
		}
		fset := token.NewFileSet()
		tree, err := parser.ParseFile(fset, f.rel, f.data, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		out = append(out, goFile{
			rel:  f.rel,
			dir:  path.Dir(f.rel),
			test: strings.HasSuffix(f.rel, "_test.go"),
			ast:  tree,
			fset: fset,
		})
	}
	return out, nil
}

// checkRegistration makes sure that every built-in item and module that
// exports New is registered. An item is registered when its registry imports
// the package and calls its New.
func checkRegistration(module string, files []goFile) []violation {
	type item struct{ class, dir, name string }
	var items []item
	seen := map[string]bool{}
	for _, f := range files {
		if f.test {
			continue
		}
		class := path.Dir(f.dir)
		if _, ok := registries[class]; !ok || path.Base(f.dir) == "all" || path.Base(f.dir) == "internal" {
			continue
		}
		for _, decl := range f.ast.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Recv == nil && fn.Name.Name == "New" && !seen[f.dir] {
				seen[f.dir] = true
				items = append(items, item{class: class, dir: f.dir, name: f.ast.Name.Name})
			}
		}
	}
	var out []violation
	for _, it := range items {
		reg := registries[it.class]
		registered := false
		for _, f := range files {
			if f.test || f.dir != reg.pkg {
				continue
			}
			names := f.importNames(func(p string) bool { return p == module+"/"+it.dir })
			if len(names) == 0 {
				continue
			}
			ast.Inspect(f.ast, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "New" {
					if x, ok := sel.X.(*ast.Ident); ok && slices.Contains(names, x.Name) {
						registered = true
					}
				}
				return true
			})
		}
		if !registered {
			file := reg.file
			if strings.Contains(file, "%s") {
				file = fmt.Sprintf(file, path.Base(it.dir))
			}
			out = append(out, violation{
				Rule: "registration",
				From: it.dir,
				To:   reg.pkg,
				Why:  fmt.Sprintf("not registered; add this line to %s/%s: %s", reg.pkg, file, fmt.Sprintf(reg.line, it.name)),
			})
		}
	}
	return out
}

// checkTestIsolation makes sure that every directory with tests has a
// TestMain that hands control to the test isolation: internal/testenv, or one
// of the ext/ test suites that wrap it.
func checkTestIsolation(module string, files []goFile) []violation {
	isolates := func(importPath string) bool {
		rel, ok := relPath(module, importPath)
		return ok && (rel == "internal/testenv" || isSuite(rel))
	}
	hasTests := map[string]bool{}
	isolated := map[string]bool{}
	for _, f := range files {
		if !f.test {
			continue
		}
		hasTests[f.dir] = true
		names := f.importNames(isolates)
		if len(names) == 0 {
			continue
		}
		for _, decl := range f.ast.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name != "TestMain" || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if x, ok := sel.X.(*ast.Ident); ok && slices.Contains(names, x.Name) && (sel.Sel.Name == "Main" || sel.Sel.Name == "Run") {
					isolated[f.dir] = true
				}
				return true
			})
		}
	}
	var out []violation
	for dir := range hasTests {
		if !isolated[dir] {
			out = append(out, violation{
				Rule: "test isolation",
				From: dir,
				To:   "internal/testenv",
				Why:  "tests here would run against the real home directory; add: func TestMain(m *testing.M) { testenv.Main(m) }",
			})
		}
	}
	return out
}

// checkBrandLiterals makes sure the product's names are not spelled out in
// string literals. They come from the generated brand package, so that a
// rename touches one file. Import paths are not names and are skipped.
func checkBrandLiterals(b brandNames, files []goFile) []violation {
	guarded := []struct{ literal, constant string }{
		{b.Prefix, "brand.Prefix"},
		{b.Home, "brand.Home"},
		{b.Name, "brand.Name"},
	}
	var out []violation
	for _, f := range files {
		imports := map[*ast.BasicLit]bool{}
		for _, spec := range f.ast.Imports {
			imports[spec.Path] = true
		}
		ast.Inspect(f.ast, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || imports[lit] {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			for _, g := range guarded {
				if strings.Contains(value, g.literal) {
					out = append(out, violation{
						Rule: "brand",
						From: f.position(lit),
						To:   strconv.Quote(g.literal),
						Why:  "names come from " + brandFile + "; use " + g.constant + " (or another constant of internal/brand) instead of the literal",
					})
				}
			}
			return true
		})
	}
	return out
}

// fileWriters are the functions of package os that create, change or remove
// files.
var fileWriters = []string{
	"WriteFile", "Create", "CreateTemp", "OpenFile", "Mkdir", "MkdirAll", "MkdirTemp",
	"Remove", "RemoveAll", "Rename", "Symlink", "Link", "Chmod", "Chown", "Lchown", "Chtimes", "Truncate",
}

// checkNoWrites is the part of rule R7 a tool can check: built-in
// implementations describe what to write and never write themselves. Whether
// the core keeps its own writes inside the executor is left to review.
func checkNoWrites(files []goFile) []violation {
	var out []violation
	for _, f := range files {
		if f.test || !under(f.dir, "builtin") {
			continue
		}
		names := f.importNames(func(p string) bool { return p == "os" })
		if len(names) == 0 {
			continue
		}
		ast.Inspect(f.ast, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok && slices.Contains(names, x.Name) && slices.Contains(fileWriters, sel.Sel.Name) {
				out = append(out, violation{
					Rule: "R7",
					From: f.dir,
					To:   "os." + sel.Sel.Name,
					Why:  "only internal/executor writes files; a built-in implementation returns operations instead (" + f.position(sel) + ")",
				})
			}
			return true
		})
	}
	return out
}

// licenseLine is the one license identifier of this repository.
const licenseLine = "SPDX-License-Identifier: Apache-2.0"

// needsLicenseHeader reports whether a file is source that has to start with
// the license header: code, scripts, and configuration in a format that has
// comments. Markdown, JSON, lock files, generated files and fixtures do not
// carry one.
func needsLicenseHeader(f sourceFile) bool {
	if inTestdata(f.rel) {
		return false
	}
	base := path.Base(f.rel)
	switch path.Ext(base) {
	case ".go":
		return !isGenerated(f.data)
	case ".sh", ".ps1", ".yaml", ".yml", ".service", ".toml", ".ts", ".tsx", ".css", ".sql":
		return true
	}
	return base == "Makefile" || base == "Dockerfile" || strings.HasPrefix(base, "Dockerfile.")
}

func checkLicenseHeaders(files []sourceFile) []violation {
	var out []violation
	for _, f := range files {
		if !needsLicenseHeader(f) {
			continue
		}
		found := false
		scanner := bufio.NewScanner(bytes.NewReader(f.data))
		for line := 0; line < 5 && scanner.Scan(); line++ {
			if strings.Contains(scanner.Text(), licenseLine) {
				found = true
				break
			}
		}
		if !found {
			out = append(out, violation{
				Rule: "license header",
				From: f.rel,
				To:   licenseLine,
				Why:  "source files start with the license header: this identifier and the copyright line, as comments",
			})
		}
	}
	return out
}

// checkStructure runs every structural rule and returns what is broken,
// sorted.
func checkStructure(s *sandbox) ([]violation, error) {
	b, err := readBrand(s.root)
	if err != nil {
		return nil, err
	}
	module, err := output(s.root, s.env, "go", "list", "-m")
	if err != nil {
		return nil, err
	}
	pkgs, err := listPackages(s, module)
	if err != nil {
		return nil, err
	}
	files, err := walkSource(s.root)
	if err != nil {
		return nil, err
	}
	goFiles, err := parseGo(files)
	if err != nil {
		return nil, err
	}
	setClosureEntries(b)
	var out []violation
	out = append(out, checkDeps(module, pkgs)...)
	out = append(out, checkNoWrites(goFiles)...)
	out = append(out, checkRegistration(module, goFiles)...)
	out = append(out, checkTestIsolation(module, goFiles)...)
	out = append(out, checkBrandLiterals(b, goFiles)...)
	out = append(out, checkLicenseHeaders(files)...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}

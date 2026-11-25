// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// The web console's third-party notices (docs: licensing, 10-release "第三方声明"). The console is
// built into web/dist and embedded in rgd-server, so what it bundles ships with the server: the
// JavaScript packages of its production dependencies and the fonts in web/public/fonts.
//
// "go run ./tools/dev web" writes them into web/dist next to the build (webNoticesFile), and
// release-files appends that file to THIRD_PARTY_NOTICES. They travel with web/dist rather than
// being worked out again by release-files because they are read from web/node_modules, which a
// release rebuild in a fresh clone does not have; web/dist is what it gets. check works them out
// too, without writing them, so that a dependency with a license that is not allowed is caught
// before it is merged.

// webNoticesFile is the name of the notices in web/dist.
const webNoticesFile = "third-party-notices.txt"

// webFontsDir holds the console's fonts, each with its license as OFL-<name>.txt.
const webFontsDir = "public/fonts"

// webPackage is a JavaScript package the console's build bundles.
type webPackage struct {
	name, version, dir string
}

// packageJSON is what is read from a package.json.
type packageJSON struct {
	Name                 string            `json:"name"`
	Version              string            `json:"version"`
	License              json.RawMessage   `json:"license"`
	Dependencies         map[string]string `json:"dependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
}

func readPackageJSON(dir string) (packageJSON, error) {
	var p packageJSON
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, fmt.Errorf("%s: %w", filepath.Join(dir, "package.json"), err)
	}
	return p, nil
}

// webPackages returns the packages that web/package.json's dependencies (not devDependencies)
// need, directly or through other packages, resolved the way Node resolves them from
// web/node_modules. A peer or optional dependency that is not installed is not bundled and is left
// out; a missing dependency is an error (run web-deps).
func webPackages(root string) ([]webPackage, error) {
	if info, err := os.Stat(filepath.Join(root, webDir, "node_modules")); err != nil || !info.IsDir() {
		return nil, errors.New("web/node_modules is missing; run first: go run ./tools/dev web-deps")
	}
	webRoot, err := filepath.EvalSymlinks(filepath.Join(root, webDir))
	if err != nil {
		return nil, err
	}
	top, err := readPackageJSON(webRoot)
	if err != nil {
		return nil, err
	}
	type want struct {
		name, from string
		required   bool
	}
	var queue []want
	for _, name := range sortedKeys(top.Dependencies) {
		queue = append(queue, want{name, webRoot, true})
	}
	seen := map[string]webPackage{} // by directory
	for len(queue) > 0 {
		w := queue[0]
		queue = queue[1:]
		dir, ok := resolveNodePackage(webRoot, w.from, w.name)
		if !ok {
			if w.required {
				return nil, fmt.Errorf("%s (needed from %s) is not installed; run: go run ./tools/dev web-deps", w.name, w.from)
			}
			continue
		}
		if _, done := seen[dir]; done {
			continue
		}
		p, err := readPackageJSON(dir)
		if err != nil {
			return nil, err
		}
		seen[dir] = webPackage{name: w.name, version: p.Version, dir: dir}
		for _, name := range sortedKeys(p.Dependencies) {
			queue = append(queue, want{name, dir, true})
		}
		for _, deps := range []map[string]string{p.PeerDependencies, p.OptionalDependencies} {
			for _, name := range sortedKeys(deps) {
				queue = append(queue, want{name, dir, false})
			}
		}
	}
	out := make([]webPackage, 0, len(seen))
	for _, p := range seen {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].name != out[j].name {
			return out[i].name < out[j].name
		}
		return out[i].version < out[j].version
	})
	return out, nil
}

// resolveNodePackage finds the package name the way Node does from the package in dir: in
// node_modules/<name> of dir and of each directory above it, up to webRoot. It returns the
// package's real directory (pnpm links packages into a store inside node_modules).
func resolveNodePackage(webRoot, dir, name string) (string, bool) {
	for d := dir; ; d = filepath.Dir(d) {
		if filepath.Base(d) != "node_modules" {
			candidate := filepath.Join(d, "node_modules", filepath.FromSlash(name))
			if isFile(filepath.Join(candidate, "package.json")) {
				real, err := filepath.EvalSymlinks(candidate)
				if err == nil {
					return real, true
				}
			}
		}
		if d == webRoot || filepath.Dir(d) == d || !strings.HasPrefix(d, webRoot) {
			return "", false
		}
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// webNotices lists every package and font the console bundles with its license texts. A license
// that is not allowed, or that cannot be recognised, is an error, as for the Go modules.
func webNotices(root string) ([]byte, error) {
	pkgs, err := webPackages(root)
	if err != nil {
		return nil, err
	}
	var out strings.Builder
	var problems []string
	section := func(title, license string, texts []string) {
		fmt.Fprintf(&out, "\n%s\n%s\nLicense: %s\n\n%s", strings.Repeat("=", 72), title, license, strings.Join(texts, "\n"))
	}
	for _, p := range pkgs {
		files, err := licenseFiles(p.dir)
		if err != nil {
			return nil, err
		}
		texts, kinds, bad, err := readLicenses(p.dir, files)
		if err != nil {
			return nil, err
		}
		for _, b := range bad {
			problems = append(problems, p.name+": "+b)
		}
		if len(files) == 0 {
			problems = append(problems, p.name+": no license file")
			continue
		}
		if len(kinds) == 0 {
			problems = append(problems, p.name+": the license could not be recognised; check it by hand and teach classifyLicense")
		}
		section(p.name+" "+p.version, strings.Join(kinds, ", "), texts)
	}

	fonts, err := filepath.Glob(filepath.Join(root, webDir, filepath.FromSlash(webFontsDir), "OFL-*.txt"))
	if err != nil {
		return nil, err
	}
	sort.Strings(fonts)
	for _, f := range fonts {
		texts, kinds, bad, err := readLicenses(filepath.Dir(f), []string{filepath.Base(f)})
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "OFL-"), ".txt")
		for _, b := range bad {
			problems = append(problems, "font "+name+": "+b)
		}
		if !slices.Contains(kinds, "OFL-1.1") {
			problems = append(problems, "font "+name+": "+filepath.Base(f)+" is not the SIL Open Font License")
		}
		section("Font "+name, strings.Join(kinds, ", "), texts)
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("%d web console package(s) or font(s) need a look:\n  %s", len(problems), strings.Join(problems, "\n  "))
	}
	return []byte(out.String()), nil
}

// readLicenses reads license files of one package and classifies them: the texts as they go into
// the notices, the allowed licenses found and what is wrong.
func readLicenses(dir string, files []string) (texts, kinds, problems []string, err error) {
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return nil, nil, nil, err
		}
		texts = append(texts, "--- "+f+"\n\n"+strings.TrimRight(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")+"\n")
		if isNoticeFile(f) {
			continue
		}
		found, forbidden := classifyLicense(string(data))
		if len(forbidden) > 0 {
			problems = append(problems, fmt.Sprintf("%s is %s, which is not allowed", f, strings.Join(forbidden, ", ")))
		}
		kinds = append(kinds, found...)
	}
	slices.Sort(kinds)
	return texts, slices.Compact(kinds), problems, nil
}

// writeWebNotices writes the notices into the built console, web/dist.
func writeWebNotices(root string) error {
	notices, err := webNotices(root)
	if err != nil {
		return fmt.Errorf("third-party notices of the web console: %w", err)
	}
	return os.WriteFile(filepath.Join(root, webDir, "dist", webNoticesFile), notices, 0o644)
}

// builtWebNotices reads the notices of the built console for release-files. Without the console
// (RELEASE_WEBDIST=0, a server with the stub page) there is nothing to list.
func builtWebNotices(root string) (string, error) {
	if os.Getenv("RELEASE_WEBDIST") == "0" {
		return "", nil
	}
	data, err := os.ReadFile(filepath.Join(root, webDir, "dist", webNoticesFile))
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("web/dist/%s is missing: build the console first with go run ./tools/dev web (or set RELEASE_WEBDIST=0 for a server without it)", webNoticesFile)
	}
	return string(data), err
}

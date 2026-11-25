// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The web console (web/) gets three kinds of generated files. The names
// (brand.gen.ts) and the design tokens (tokens.gen.css) are generated here in
// Go, so that anyone can regenerate them without Node. The API types
// (schema.gen.ts) come from openapi-typescript, which needs Node and the
// console's dependencies; without them that one file is reported as not
// run, never as up to date.

const (
	webDir         = "web"
	webPackageFile = "web/package.json"
	brandTSOutput  = "web/src/brand.gen.ts"
	apiTSOutput    = "web/src/api/schema.gen.ts"
)

// hasWebConsole reports whether the tree has the console's sources. Trees
// without it (a test's temporary root) get none of its generated files.
func hasWebConsole(root string) bool {
	return isFile(filepath.Join(root, filepath.FromSlash(webPackageFile)))
}

// brandTS is the names for the console, in the same order as the Go
// constants.
func brandTS(b brandNames) []byte {
	var buf bytes.Buffer
	buf.WriteString(webHeader("//", "", brandFile))
	buf.WriteString("\n/** The names of the product, from brand.json. Never spell them out elsewhere. */\nexport const BRAND = {\n")
	for _, f := range b.fields() {
		value, _ := json.Marshal(f.value) // a string always marshals
		fmt.Fprintf(&buf, "  /** %s */\n  %s: %s,\n", strings.ToUpper(f.doc[:1])+f.doc[1:], f.key, value)
	}
	buf.WriteString("} as const\n")
	return buf.Bytes()
}

// webSkipped is a generated file gen could not produce on this machine, and
// why. It is reported as not run.
type webSkipped struct {
	path, reason string
}

// nodeTool locates a command-line tool installed in web/node_modules and the
// node binary to run it with. It returns errNotRun when either is missing:
// that is a machine without the console's toolchain, not a broken tree.
func nodeTool(root, pkg string) (node, script string, err error) {
	node, lookErr := exec.LookPath("node")
	if lookErr != nil {
		return "", "", errNotRun{"no Node on PATH"}
	}
	dir := filepath.Join(root, webDir, "node_modules", filepath.FromSlash(pkg))
	data, readErr := os.ReadFile(filepath.Join(dir, "package.json"))
	if readErr != nil {
		return "", "", errNotRun{"web/node_modules has no " + pkg + "; run first: go run ./tools/dev web-deps"}
	}
	var manifest struct {
		Bin json.RawMessage `json:"bin"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", "", fmt.Errorf("%s/package.json: %w", pkg, err)
	}
	var single string
	var named map[string]string
	switch {
	case json.Unmarshal(manifest.Bin, &single) == nil && single != "":
		script = single
	case json.Unmarshal(manifest.Bin, &named) == nil && named[pathBase(pkg)] != "":
		script = named[pathBase(pkg)]
	default:
		return "", "", fmt.Errorf("%s/package.json names no command", pkg)
	}
	return node, filepath.Join(dir, filepath.FromSlash(script)), nil
}

func pathBase(pkg string) string {
	if i := strings.LastIndex(pkg, "/"); i >= 0 {
		return pkg[i+1:]
	}
	return pkg
}

// generateAPITypes runs openapi-typescript on the contract. The output gets
// the license header and the generated-code marker in front.
func generateAPITypes(root string, env []string) (generated, error) {
	node, script, err := nodeTool(root, "openapi-typescript")
	if err != nil {
		return generated{}, err
	}
	cmd := exec.Command(node, script, filepath.FromSlash(apiSpec))
	cmd.Dir = root
	cmd.Env = env
	if env == nil {
		cmd.Env = os.Environ()
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return generated{}, fmt.Errorf("openapi-typescript %s: %w\n%s", apiSpec, err, strings.TrimSpace(stderr.String()))
	}
	if stdout.Len() == 0 {
		return generated{}, errors.New("openapi-typescript printed nothing")
	}
	out := webHeader("//", "", apiSpec) + "\n" + stdout.String()
	return generated{path: apiTSOutput, data: []byte(out)}, nil
}

// generateWeb returns the console's generated files: the ones Go can make,
// and the API types when Node can make them. What could not be made is in
// skipped.
func generateWeb(root string, env []string, b brandNames) (files []generated, skipped []webSkipped, err error) {
	if !hasWebConsole(root) {
		for _, out := range []string{brandTSOutput, apiTSOutput} {
			if isFile(filepath.Join(root, filepath.FromSlash(out))) {
				return nil, nil, fmt.Errorf("%s is generated for the web console, but %s is missing", out, webPackageFile)
			}
		}
		return nil, nil, nil
	}
	files = append(files, generated{path: brandTSOutput, data: brandTS(b)})
	tokens, err := generateTokens(root)
	if err != nil {
		return nil, nil, err
	}
	files = append(files, tokens...)
	if !isFile(filepath.Join(root, filepath.FromSlash(apiSpec))) {
		if isFile(filepath.Join(root, filepath.FromSlash(apiTSOutput))) {
			return nil, nil, fmt.Errorf("%s is generated from %s, which is missing", apiTSOutput, apiSpec)
		}
		return files, nil, nil
	}
	api, err := generateAPITypes(root, env)
	var notRun errNotRun
	switch {
	case errors.As(err, &notRun):
		skipped = append(skipped, webSkipped{apiTSOutput, notRun.reason})
	case err != nil:
		return nil, nil, err
	default:
		files = append(files, api)
	}
	return files, skipped, nil
}

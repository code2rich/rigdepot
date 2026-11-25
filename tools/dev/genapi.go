// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// apiSpec is the HTTP contract everything in apiTargets is generated from.
const apiSpec = "api/openapi.yaml"

// apiTargets are the files oapi-codegen generates from the contract, each
// with its configuration. The configurations name no output file: the
// generator prints the code and gen decides where it goes, so that --check
// can compare without writing anything.
var apiTargets = []struct{ config, output string }{
	{"api/server.codegen.yaml", "internal/server/httpapi/api.gen.go"},
	{"api/client.codegen.yaml", "internal/apiclient/api.gen.go"},
}

// oapiCodegen is the generator's name in toolsModfile, where its version is
// pinned.
const oapiCodegen = "oapi-codegen"

// generateAPI runs oapi-codegen for every target. A tree without the
// contract and without any of its outputs (a test's temporary root) has
// nothing to generate; a tree that lost the contract but still has outputs
// is an error, so that deleting the contract cannot leave stale code behind
// unnoticed.
func generateAPI(root string, env []string) ([]generated, error) {
	if !isFile(filepath.Join(root, filepath.FromSlash(apiSpec))) {
		for _, t := range apiTargets {
			if isFile(filepath.Join(root, filepath.FromSlash(t.output))) {
				return nil, fmt.Errorf("%s is generated from %s, which is missing", t.output, apiSpec)
			}
		}
		return nil, nil
	}
	var out []generated
	for _, t := range apiTargets {
		src, err := runOapiCodegen(root, env, t.config)
		if err != nil {
			return nil, err
		}
		out = append(out, generated{path: t.output, data: src})
	}
	return out, nil
}

// runOapiCodegen runs the pinned generator with one configuration and
// returns the code it prints.
func runOapiCodegen(root string, env []string, config string) ([]byte, error) {
	args := goTool(toolsModfile, oapiCodegen, "-config", filepath.FromSlash(config), filepath.FromSlash(apiSpec))
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	cmd.Env = env
	if env == nil {
		cmd.Env = os.Environ()
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go %s: %w\n%s\n(if the generator could not be built, run first: go run ./tools/dev deps)",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	if stdout.Len() == 0 {
		return nil, fmt.Errorf("%s printed nothing for %s", oapiCodegen, config)
	}
	return stdout.Bytes(), nil
}

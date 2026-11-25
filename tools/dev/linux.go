// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var goDirective = regexp.MustCompile(`(?m)^go (\d+\.\d+(?:\.\d+)?)\s*$`)

// goVersion returns the Go version go.mod asks for.
func goVersion(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	m := goDirective.FindSubmatch(data)
	if m == nil {
		return "", errors.New("go.mod has no go directive")
	}
	return string(m[1]), nil
}

// linuxImageVariable is the suffix of the environment variable that replaces
// the container image check-linux uses, for a registry mirror or an image
// that is already on the machine.
const linuxImageVariable = "DEV_LINUX_IMAGE"

// runCheckLinux runs check inside a Linux container on this machine, so that
// the tests are really executed on Linux and not just compiled for it. The
// repository and the module cache are mounted read-only; the container has
// no need for the network once the image is there.
//
// The image is the official Go image for the version in go.mod, unless the
// environment names another one.
func runCheckLinux(root string, args []string) error {
	if len(args) != 0 {
		return errors.New("check-linux takes no arguments")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return errors.New("not run: docker was not found; this change cannot claim to be verified on Linux")
	}
	version, err := goVersion(root)
	if err != nil {
		return err
	}
	modCache, err := goEnv(root, "GOMODCACHE")
	if err != nil {
		return err
	}
	b, err := readBrand(root)
	if err != nil {
		return err
	}
	image := "golang:" + version
	if override := os.Getenv(b.EnvPrefix + linuxImageVariable); override != "" {
		image = override
	}
	cmd := exec.Command("docker", "run", "--rm",
		"--volume", root+":/src:ro",
		"--volume", modCache+":/go/pkg/mod:ro",
		"--volume", b.CLILong+"-dev-gocache:/gocache",
		"--env", "GOCACHE=/gocache",
		"--env", "GOFLAGS=-buildvcs=false",
		"--workdir", "/src",
		image, "go", "run", "./tools/dev", "check")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	fmt.Printf("==> %s\n", strings.Join(cmd.Args, " "))
	return cmd.Run()
}

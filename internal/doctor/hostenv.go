// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package doctor

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/code2rich/rigdepot/ext/adapter"
)

// VersionTimeout limits how long a tool may take to print its version.
const VersionTimeout = 10 * time.Second

// HostAdapterEnv returns what adapters see of the real computer: files read from the home
// directory userHome (paths "~/…") or from absolute paths, executables found on PATH, and
// "<tool> --version" run without a shell and with a timeout.
//
// TODO(M0-26): the sync engine builds the same Env for detection; once it is merged, doctor
// takes that one, so that both commands see a tool the same way.
func HostAdapterEnv(goos, userHome string, getenv func(string) string) adapter.Env {
	resolve := func(p string) (string, error) {
		switch {
		case p == "~" || strings.HasPrefix(p, "~/"):
			if userHome == "" {
				return "", fmt.Errorf("%s: the home directory is unknown: %w", p, fs.ErrNotExist)
			}
			rel := path.Clean(strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
			if rel == ".." || strings.HasPrefix(rel, "../") {
				return "", fmt.Errorf("%s: %w (outside the home directory)", p, fs.ErrPermission)
			}
			return filepath.Join(userHome, filepath.FromSlash(rel)), nil
		case filepath.IsAbs(filepath.FromSlash(p)):
			return filepath.Clean(filepath.FromSlash(p)), nil
		}
		return "", fmt.Errorf("%s: %w (not \"~/…\" and not absolute)", p, fs.ErrInvalid)
	}
	return adapter.Env{
		GOOS:   goos,
		Home:   userHome,
		Getenv: getenv,
		ReadFile: func(p string) ([]byte, error) {
			full, err := resolve(p)
			if err != nil {
				return nil, err
			}
			return os.ReadFile(full)
		},
		Stat: func(p string) (fs.FileInfo, error) {
			full, err := resolve(p)
			if err != nil {
				return nil, err
			}
			return os.Stat(full)
		},
		LookPath: exec.LookPath,
		Version: func(ctx context.Context, bin string, args ...string) (string, error) {
			ctx, cancel := context.WithTimeout(ctx, VersionTimeout)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, args...)
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			err := cmd.Run()
			return out.String(), err
		},
	}
}

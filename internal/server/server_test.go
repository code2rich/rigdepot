// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"bytes"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

func TestVersion(t *testing.T) {
	for _, arg := range []string{"--version", "version"} {
		var stdout, stderr bytes.Buffer
		if code := run([]string{arg}, &stdout, &stderr); code != 0 {
			t.Errorf("%s: exit code %d, want 0", arg, code)
		}
		if !strings.HasPrefix(stdout.String(), brand.Server+" ") || stderr.Len() != 0 {
			t.Errorf("%s: stdout = %q, stderr = %q; want the version on stdout only", arg, stdout.String(), stderr.String())
		}
	}
}

func TestAnythingElseFailsWithoutOutputOnStdout(t *testing.T) {
	for _, args := range [][]string{nil, {"serve"}, {"--version", "extra"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Errorf("%v: exit code %d, want 2", args, code)
		}
		if stdout.Len() != 0 || stderr.Len() == 0 {
			t.Errorf("%v: stdout = %q, stderr = %q; want an explanation on stderr only", args, stdout.String(), stderr.String())
		}
	}
}

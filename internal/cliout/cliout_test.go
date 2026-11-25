// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cliout

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// The numbers are a contract with scripts: they must never change.
func TestExitCodesAreFixed(t *testing.T) {
	want := map[ExitCode]int{
		ExitOK: 0, ExitFailure: 1, ExitUsage: 2, ExitPending: 3, ExitCheckFailed: 4,
		ExitDiff: 5, ExitAuth: 6, ExitUnreachable: 7, ExitIncompatible: 8, ExitStateUnknown: 9,
	}
	for code, n := range want {
		if int(code) != n {
			t.Errorf("exit code constant %d should be %d", code, n)
		}
	}
}

func TestExitOf(t *testing.T) {
	tests := []struct {
		err  error
		want ExitCode
	}{
		{nil, ExitOK},
		{errors.New("boom"), ExitFailure},
		{Usage("bad_flag", "x"), ExitUsage},
		{Fail(ExitStateUnknown, "manifest_unreuserble", "x"), ExitStateUnknown},
		{fmt.Errorf("wrapped: %w", Fail(ExitUnreachable, "unreachable", "x")), ExitUnreachable},
		{Status(ExitDiff), ExitDiff},
		{fmt.Errorf("wrapped: %w", Status(ExitPending)), ExitPending},
	}
	for _, tt := range tests {
		if got := ExitOf(tt.err); got != tt.want {
			t.Errorf("ExitOf(%v) = %d, want %d", tt.err, got, tt.want)
		}
	}
}

func newOutput(opts Options) (*Output, *bytes.Buffer, *bytes.Buffer) {
	var stdout, stderr bytes.Buffer
	return New(&stdout, &stderr, opts), &stdout, &stderr
}

// decodeOne decodes standard output and fails unless it is exactly one JSON
// object.
func decodeOne(t *testing.T, data []byte) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("standard output is not a JSON object: %v\n%s", err, data)
	}
	if _, err := dec.Token(); err != io.EOF {
		t.Fatalf("standard output has more than one JSON document:\n%s", data)
	}
	return doc
}

type sample struct {
	Name  string   `json:"name"`
	Items []string `json:"items"`
}

func TestResultJSON(t *testing.T) {
	o, stdout, stderr := newOutput(Options{JSON: true, Color: true})
	human := func(io.Writer) error { t.Error("human output written in JSON mode"); return nil }
	if err := o.Result(Schema("sample", 2), sample{"a<b>&c", []string{"x"}}, human); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stdout.String(), `{"schema":"`+brand.CLI+`.sample/v2","name":"a<b>&c"`) {
		t.Errorf("schema is not the first field, or text was escaped: %s", stdout)
	}
	doc := decodeOne(t, stdout.Bytes())
	if doc["schema"] != brand.CLI+".sample/v2" {
		t.Errorf("schema = %v", doc["schema"])
	}
	if err := o.Result(Schema("sample", 2), sample{}, human); !errors.Is(err, ErrResultWritten) {
		t.Errorf("second Result: %v, want ErrResultWritten", err)
	}
	if strings.Contains(stdout.String(), "\x1b[") || stderr.Len() != 0 {
		t.Errorf("colours or stray output: stdout %q, stderr %q", stdout, stderr)
	}
	decodeOne(t, stdout.Bytes())
}

func TestResultRejectsWhatIsNotAnObject(t *testing.T) {
	for name, data := range map[string]any{
		"a list":            []string{"x"},
		"a string":          "x",
		"its own schema":    map[string]string{"schema": "mine"},
		"a function, no JS": func() {},
	} {
		o, stdout, _ := newOutput(Options{JSON: true})
		if err := o.Result("x/v1", data, nil); err == nil || stdout.Len() != 0 || o.Done() {
			t.Errorf("%s: err = %v, stdout %q", name, err, stdout)
		}
	}
	o, stdout, _ := newOutput(Options{JSON: true})
	if err := o.Result("x/v1", nil, nil); err != nil || stdout.String() != `{"schema":"x/v1"}`+"\n" {
		t.Errorf("empty result: %q, %v", stdout, err)
	}
}

func TestResultHuman(t *testing.T) {
	o, stdout, _ := newOutput(Options{})
	if err := o.Result("x/v1", sample{}, func(w io.Writer) error { _, err := io.WriteString(w, "done\n"); return err }); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "done\n" || !o.Done() {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestFailJSON(t *testing.T) {
	o, stdout, stderr := newOutput(Options{JSON: true})
	e := Fail(ExitStateUnknown, "manifest_unreuserble", "清单读不到")
	e.Hint = "运行 rgd doctor"
	if code := o.Fail(e); code != ExitStateUnknown {
		t.Errorf("exit code %d", code)
	}
	doc := decodeOne(t, stdout.Bytes())
	if doc["schema"] != Schema("error", 1) {
		t.Errorf("schema = %v", doc["schema"])
	}
	inner, _ := doc["error"].(map[string]any)
	if inner["code"] != "manifest_unreuserble" || inner["message"] != "清单读不到" || inner["exit"] != float64(9) {
		t.Errorf("error = %v", inner)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q; the error is in the JSON document", stderr)
	}
}

func TestFailAfterResultKeepsOneDocument(t *testing.T) {
	o, stdout, stderr := newOutput(Options{JSON: true})
	o.Result("x/v1", sample{}, nil)
	if code := o.Fail(errors.New("late failure")); code != ExitFailure {
		t.Errorf("exit code %d", code)
	}
	decodeOne(t, stdout.Bytes())
	if !strings.Contains(stderr.String(), "late failure") {
		t.Errorf("stderr = %q; the failure must still be reported", stderr)
	}
}

func TestFailStatusPrintsNothing(t *testing.T) {
	o, stdout, stderr := newOutput(Options{})
	o.Result("x/v1", nil, func(w io.Writer) error { return nil })
	if code := o.Fail(Status(ExitPending)); code != ExitPending || stdout.Len()+stderr.Len() != 0 {
		t.Errorf("code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestFailHuman(t *testing.T) {
	o, stdout, stderr := newOutput(Options{})
	e := Usage("unknown_command", "no such command")
	e.Hint = "run --help"
	if code := o.Fail(e); code != ExitUsage {
		t.Errorf("exit code %d", code)
	}
	if want := brand.CLI + ": no such command\nrun --help\n"; stderr.String() != want || stdout.Len() != 0 {
		t.Errorf("stderr %q (want %q), stdout %q", stderr, want, stdout)
	}
	o, _, stderr = newOutput(Options{})
	if code := o.Fail(errors.New("boom")); code != ExitFailure || !strings.Contains(stderr.String(), "boom") {
		t.Errorf("plain error: code %d, stderr %q", code, stderr)
	}
}

func TestVerbosity(t *testing.T) {
	for _, tt := range []struct {
		v    Verbosity
		want string
	}{
		{Quiet, "warn\n"},
		{Normal, "info\nwarn\n"},
		{Verbose, "info\nwarn\ndebug\n"},
	} {
		o, stdout, stderr := newOutput(Options{Verbosity: tt.v})
		o.Info("info")
		o.Warn("warn")
		o.Debug("debug")
		if stderr.String() != tt.want || stdout.Len() != 0 {
			t.Errorf("verbosity %d: stderr %q, want %q", tt.v, stderr, tt.want)
		}
	}
}

func TestColour(t *testing.T) {
	if !ColorEnabled(false, false, true) || ColorEnabled(true, false, true) || ColorEnabled(false, true, true) || ColorEnabled(false, false, false) {
		t.Error("ColorEnabled: colours only on a terminal, without --no-color and without --json")
	}
	on, _, _ := newOutput(Options{Color: true})
	off, _, _ := newOutput(Options{})
	if got := on.Paint(Red, "x"); got != "\x1b[31mx\x1b[0m" {
		t.Errorf("Paint with colours = %q", got)
	}
	if got := off.Paint(Red, "x"); got != "x" {
		t.Errorf("Paint without colours = %q", got)
	}
}

func TestDisplayWidth(t *testing.T) {
	for s, want := range map[string]int{"": 0, "abc": 3, "配置": 4, "a配b": 4, "（未设置）": 10, "ｒｇｄ": 6} {
		if got := DisplayWidth(s); got != want {
			t.Errorf("DisplayWidth(%q) = %d, want %d", s, got, want)
		}
	}
	if got := PadRight("配置", 6) + "|"; got != "配置  |" {
		t.Errorf("PadRight = %q", got)
	}
}

func TestShowPath(t *testing.T) {
	tests := []struct {
		path, home, goos, want string
	}{
		{"/home/user/.claude/skills", "/home/user", "linux", "~/.claude/skills"},
		{"/home/user", "/home/user/", "linux", "~"},
		{"/home/someone/x", "/home/user", "linux", "/home/someone/x"},
		{"/etc/x", "/home/user", "darwin", "/etc/x"},
		{`C:\Users\User\.codex\config.toml`, `C:\Users\User`, "windows", `%USERPROFILE%\.codex\config.toml`},
		{`c:\users\user\x`, `C:\Users\User`, "windows", `%USERPROFILE%\x`},
		{`C:/Users/User/x`, `C:\Users\User`, "windows", `%USERPROFILE%\x`},
		{`D:\x`, `C:\Users\User`, "windows", `D:\x`},
		{"/x", "", "linux", "/x"},
	}
	for _, tt := range tests {
		if got := ShowPath(tt.path, tt.home, tt.goos); got != tt.want {
			t.Errorf("ShowPath(%q, %q, %s) = %q, want %q", tt.path, tt.home, tt.goos, got, tt.want)
		}
	}
}

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package doctor

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"

	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
	"github.com/code2rich/rigdepot/internal/localconfig"
)

// configCheck: the local configuration file, if there is one, can be read and parsed, and the
// environment variables that set configuration hold valid values. A file that cannot be used is
// a failure, never an empty configuration.
type configCheck struct{}

func (configCheck) ID() string { return "config" }

func (configCheck) Title() string { return msg.DoctorConfigTitle }

func (configCheck) Run(_ context.Context, env *Env) []Result {
	if !env.homeKnown() {
		return []Result{skippedNoHome()}
	}
	p := env.Home.Join(home.ConfigFile)
	shown := i18n.V("path", env.show(p))
	cfg, err := localconfig.Load(p, env.getenv)

	var out []Result
	for _, e := range flatten(err) {
		var fe *localconfig.FileError
		var ee *localconfig.EnvError
		switch {
		case errors.As(e, &fe):
			out = append(out, Result{Status: Fail, Code: "unreadable", Path: p,
				Message: M(msg.DoctorConfigUnreadable, shown, i18n.V("detail", fe.Err.Error())),
				Hint:    M(msg.DoctorConfigUnreadableHint)})
		case errors.As(e, &ee):
			out = append(out, Result{Status: Fail, Code: "env_invalid",
				Message: M(msg.DoctorConfigEnvInvalid, i18n.V("name", ee.Name), i18n.V("value", ee.Err.Value)),
				Hint:    M(msg.DoctorConfigEnvInvalidHint, i18n.V("name", ee.Name)),
				Facts:   map[string]string{"variable": ee.Name}})
		default:
			out = append(out, Result{Status: Fail, Code: "unreadable", Path: p,
				Message: M(msg.DoctorConfigUnreadable, shown, i18n.V("detail", e.Error())),
				Hint:    M(msg.DoctorConfigUnreadableHint)})
		}
	}
	if len(out) > 0 {
		return out
	}

	var ignored []string
	for _, w := range cfg.Warnings() {
		// A key this version does not know may have been written by a newer one; only keys
		// that this version knows and refuses in the file are worth a warning.
		if w.Kind == localconfig.WarnFlagOnly || w.Kind == localconfig.WarnNotInFile {
			ignored = append(ignored, w.Key)
		}
	}
	if len(ignored) > 0 {
		return []Result{{Status: Warn, Code: "ignored_keys", Path: p,
			Message: M(msg.DoctorConfigIgnored, shown, i18n.V("keys", strings.Join(ignored, ", "))),
			Hint:    M(msg.DoctorConfigIgnoredHint)}}
	}
	if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
		return []Result{{Status: OK, Code: "missing", Path: p, Message: M(msg.DoctorConfigMissing, shown)}}
	}
	return []Result{{Status: OK, Code: "ok", Path: p, Message: M(msg.DoctorConfigOk, shown)}}
}

// flatten returns the errors joined in err, one by one.
func flatten(err error) []error {
	if err == nil {
		return nil
	}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		var out []error
		for _, e := range j.Unwrap() {
			out = append(out, flatten(e)...)
		}
		return out
	}
	return []error{err}
}

// skippedNoHome is the result of a check that looks inside the local data directory when where
// that is cannot be known. The home check reports the cause.
func skippedNoHome() Result {
	return Result{Status: Skipped, Code: "no_home", Message: M(msg.DoctorSkippedNoHome)}
}

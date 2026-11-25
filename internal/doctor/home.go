// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package doctor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
)

// homeCheck: the local data directory is a directory that only its owner can open, and the
// records in it are files only the owner can read. A directory that does not exist yet is fine:
// the first sync creates it.
type homeCheck struct{}

func (homeCheck) ID() string { return "home" }

func (homeCheck) Title() string { return msg.DoctorHomeTitle }

// privateFiles are the files in the local data directory that must be readable by the owner
// alone.
var privateFiles = []string{home.ConfigFile, home.ManifestFile, home.ManifestPrevious, home.JournalFile}

func (homeCheck) Run(_ context.Context, env *Env) []Result {
	if !env.homeKnown() {
		detail := "?"
		if env.HomeErr != nil {
			detail = env.HomeErr.Error()
		}
		return []Result{{
			Status:  Fail,
			Code:    "no_home",
			Message: M(msg.DoctorHomeUnknown, i18n.V("detail", detail)),
			Hint:    M(msg.DoctorHomeUnknownHint, i18n.V("env", home.EnvVar)),
		}}
	}
	p := env.Home.Path()
	shown := i18n.V("path", env.show(p))
	fi, err := os.Stat(p) // a link to the directory is fine: it is where the person put it
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return []Result{{Status: OK, Code: "missing", Path: p, Message: M(msg.DoctorHomeMissing, shown)}}
	case err != nil:
		return []Result{unreadable(p, err, M(msg.DoctorHomeUnreadable, shown, i18n.V("detail", err.Error())))}
	case !fi.IsDir():
		return []Result{{Status: Fail, Code: "not_dir", Path: p,
			Message: M(msg.DoctorHomeNotDir, shown), Hint: M(msg.DoctorHomeNotDirHint)}}
	}
	if env.GOOS == "windows" {
		// No permission bits: the directory inherits the ACL of the home directory.
		return []Result{{Status: OK, Code: "ok", Path: p, Message: M(msg.DoctorHomeExists, shown)}}
	}

	var out []Result
	if fi.Mode().Perm()&^home.DirPerm != 0 {
		out = append(out, tooOpen(env, p, fi.Mode().Perm(), home.DirPerm))
	}
	for _, name := range privateFiles {
		fp := env.Home.Join(name)
		fi, err := os.Lstat(fp)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			out = append(out, unreadable(fp, err, M(msg.DoctorHomeUnreadable,
				i18n.V("path", env.show(fp)), i18n.V("detail", err.Error()))))
		case fi.Mode().IsRegular() && fi.Mode().Perm()&^home.FilePerm != 0:
			out = append(out, tooOpen(env, fp, fi.Mode().Perm(), home.FilePerm))
		}
	}
	if len(out) == 0 {
		out = append(out, Result{Status: OK, Code: "ok", Path: p, Message: M(msg.DoctorHomeOk, shown)})
	}
	return out
}

// tooOpen is the warning for a file or directory that others can open.
func tooOpen(env *Env, p string, have, want fs.FileMode) Result {
	shown := i18n.V("path", env.show(p))
	return Result{
		Status:  Warn,
		Code:    "too_open",
		Path:    p,
		Message: M(msg.DoctorHomeTooOpen, shown, i18n.V("mode", octal(have))),
		Hint:    M(msg.DoctorHomeChmodHint, shown, i18n.V("mode", octal(want))),
		Facts:   map[string]string{"mode": octal(have)},
	}
}

// unreadable is the result for something that is there and cannot be looked at.
func unreadable(p string, err error, m Message) Result {
	code := "unreadable"
	if errors.Is(err, fs.ErrPermission) {
		code = "permission_denied"
	}
	return Result{Status: Unknown, Code: code, Path: p, Message: m}
}

func octal(m fs.FileMode) string { return fmt.Sprintf("%04o", uint32(m.Perm())) }

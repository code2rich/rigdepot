// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package doctor

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strconv"

	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
	"github.com/code2rich/rigdepot/internal/manifest"
)

// manifestCheck: the manifest can be read and trusted. A manifest that does not exist is empty
// (nothing was written yet); one that exists and cannot be read, parsed or understood makes the
// local state unknown, and sync and uninstall refuse to act until it is fixed.
type manifestCheck struct{}

func (manifestCheck) ID() string { return "manifest" }

func (manifestCheck) Title() string { return msg.DoctorManifestTitle }

func (manifestCheck) Run(_ context.Context, env *Env) []Result {
	if !env.homeKnown() {
		return []Result{skippedNoHome()}
	}
	p := env.Home.Join(home.ManifestFile)
	shown := i18n.V("path", env.show(p))
	if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
		return []Result{{Status: OK, Code: "missing", Path: p, Message: M(msg.DoctorManifestMissing, shown)}}
	}
	m, err := manifest.ReadFile(p)
	if err == nil {
		n := len(m.Entries)
		return []Result{{Status: OK, Code: "ok", Path: p,
			Message: M(msg.DoctorManifestOk, shown, i18n.Count(n)),
			Facts:   map[string]string{"entries": strconv.Itoa(n)}}}
	}

	reason, detail := manifest.Unreadable, err.Error()
	var ue *manifest.UnknownError
	if errors.As(err, &ue) {
		reason = ue.Reason
		if ue.Err != nil {
			detail = ue.Err.Error()
		}
	}
	res := Result{Status: Fail, Code: "manifest_" + string(reason), Path: p,
		Facts: map[string]string{"reason": string(reason)}}
	d := i18n.V("detail", detail)
	switch reason {
	case manifest.Newer:
		res.Message = M(msg.DoctorManifestNewer, shown)
		res.Hint = M(msg.DoctorManifestNewerHint)
		return []Result{res}
	case manifest.Malformed:
		res.Message = M(msg.DoctorManifestMalformed, shown, d)
	case manifest.Invalid:
		res.Message = M(msg.DoctorManifestInvalid, shown, d)
	default:
		res.Message = M(msg.DoctorManifestUnreadable, shown, d)
	}
	res.Hint = M(msg.DoctorManifestUnknownHint)
	if prev := env.Home.Join(home.ManifestPrevious); exists(prev) {
		res.Hint = M(msg.DoctorManifestPreviousHint, i18n.V("previous", env.show(prev)))
	}
	return []Result{res}
}

// exists reports whether something is at p.
func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

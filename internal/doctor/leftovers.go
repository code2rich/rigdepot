// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package doctor

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
	"github.com/code2rich/rigdepot/internal/sandbox"
)

// leftoversCheck: no temporary files are left in the local data directory by runs that did not
// finish: preview sandboxes in tmp/ and the temporary files of atomic writes (".<name>.tmp-…")
// next to the records. Only what is older than sandbox.StaleAfter counts, so that the files of
// a run in progress are not reported.
type leftoversCheck struct{}

func (leftoversCheck) ID() string { return "leftovers" }

func (leftoversCheck) Title() string { return msg.DoctorLeftoversTitle }

// atomicTemp is part of the name of a temporary file written next to its target and renamed
// over it (home.WriteFileAtomic, the local configuration).
const atomicTemp = ".tmp-"

func (leftoversCheck) Run(_ context.Context, env *Env) []Result {
	if !env.homeKnown() {
		return []Result{skippedNoHome()}
	}
	cutoff := env.now().Add(-sandbox.StaleAfter)
	var found []string
	var out []Result
	look := func(dir string, match func(name string) bool) {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			return
		}
		if err != nil {
			out = append(out, unreadable(dir, err, M(msg.DoctorLeftoversUnreadable,
				i18n.V("path", env.show(dir)), i18n.V("detail", err.Error()))))
			return
		}
		for _, e := range entries {
			if !match(e.Name()) {
				continue
			}
			info, err := e.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue // gone meanwhile, or possibly still in use
			}
			found = append(found, filepath.Join(dir, e.Name()))
		}
	}
	everything := func(string) bool { return true }
	atomic := func(name string) bool { return strings.Contains(name, atomicTemp) }
	look(env.Home.Join(home.TmpDir), everything)
	look(env.Home.Path(), atomic)
	look(env.Home.Join(home.StateDir), atomic)

	if len(found) > 0 {
		res := Result{Status: Warn, Code: "found",
			Message: M(msg.DoctorLeftoversFound, i18n.Count(len(found))),
			Hint:    M(msg.DoctorLeftoversFoundHint),
			Facts:   map[string]string{"count": strconv.Itoa(len(found))}}
		for _, p := range found {
			res.Details = append(res.Details, M(msg.DoctorLeftoversItem, i18n.V("path", env.show(p))))
		}
		out = append(out, res)
	}
	if len(out) == 0 {
		out = append(out, Result{Status: OK, Code: "none", Message: M(msg.DoctorLeftoversNone)})
	}
	return out
}

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

// intentsCheck: the intent log holds no write that was started and never recorded. Such writes
// are left by a run that was interrupted; the next sync settles every one whose target it can
// recognise (manifest.Locked.Recover), and the ones it cannot (Recovery.Unresolved) stay in the
// log for a person to look at. This check lists whatever is in the log.
//
// Reading the log needs the lock, so that a run in progress is not mistaken for an interrupted
// one. The lock is only taken when there is a log: in the usual case nothing is touched at all.
type intentsCheck struct{}

func (intentsCheck) ID() string { return "intents" }

func (intentsCheck) Title() string { return msg.DoctorIntentsTitle }

func (intentsCheck) Run(ctx context.Context, env *Env) []Result {
	if !env.homeKnown() {
		return []Result{skippedNoHome()}
	}
	p := env.Home.Join(home.JournalFile)
	shown := i18n.V("path", env.show(p))
	none := Result{Status: OK, Code: "none", Message: M(msg.DoctorIntentsNone)}
	if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
		return []Result{none}
	}

	l, err := manifest.Open(env.Home).Lock(ctx, env.lockWait())
	if errors.Is(err, manifest.ErrLocked) {
		return []Result{{Status: Unknown, Code: "busy", Message: M(msg.DoctorIntentsBusy), Hint: M(msg.DoctorIntentsBusyHint)}}
	}
	if err != nil {
		return []Result{{Status: Unknown, Code: "unreadable", Path: p,
			Message: M(msg.DoctorIntentsUnreadable, shown, i18n.V("detail", err.Error()))}}
	}
	defer l.Release()

	pending, err := l.Pending()
	if err != nil {
		return []Result{{Status: Fail, Code: "unreadable", Path: p,
			Message: M(msg.DoctorIntentsUnreadable, shown, i18n.V("detail", err.Error())),
			Hint:    M(msg.DoctorIntentsUnreadableHint)}}
	}
	if len(pending) == 0 {
		return []Result{none}
	}
	res := Result{Status: Warn, Code: "pending", Path: p,
		Message: M(msg.DoctorIntentsPending, i18n.Count(len(pending))),
		Hint:    M(msg.DoctorIntentsPendingHint),
		Facts:   map[string]string{"count": strconv.Itoa(len(pending))}}
	for _, in := range pending {
		key := msg.DoctorIntentsItemWrite
		if in.Kind == manifest.Remove {
			key = msg.DoctorIntentsItemRemove
		}
		res.Details = append(res.Details, M(key,
			i18n.V("path", in.Entry.Path),
			i18n.V("tool", in.Entry.Tool),
			i18n.V("at", in.At.Local().Format("2006-01-02 15:04"))))
	}
	return []Result{res}
}

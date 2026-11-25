// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package doctor

import (
	"context"
	"errors"
	"time"

	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
	"github.com/code2rich/rigdepot/internal/keyring"
)

// keyringCheck: where the device key and token are kept, and whether they can be reached. The
// system keychain is fine; files are a warning every time, because anyone who can read them can
// act as this device; a keychain that holds them and does not answer is a failure.
type keyringCheck struct{}

func (keyringCheck) ID() string { return "keyring" }

func (keyringCheck) Title() string { return msg.DoctorKeyringTitle }

// keyringTimeout limits the look at the system keychain. A keychain that wants to be unlocked
// may ask for a password; the check does not wait long for it.
const keyringTimeout = 15 * time.Second

func (keyringCheck) Run(_ context.Context, env *Env) []Result {
	if !env.homeKnown() {
		return []Result{skippedNoHome()}
	}
	probe := env.Keyring
	if probe == nil {
		probe = keyring.Open(env.Home, keyring.Options{Timeout: keyringTimeout}).Probe
	}
	st := probe()
	facts := map[string]string{"kind": st.Kind.String()}
	if st.Err != nil {
		detail := i18n.V("detail", st.Err.Error())
		if errors.Is(st.Err, keyring.ErrUnavailable) {
			return []Result{{Status: Fail, Code: "unavailable", Facts: facts,
				Message: M(msg.DoctorKeyringUnavailable, detail), Hint: M(msg.DoctorKeyringUnavailableHint)}}
		}
		return []Result{{Status: Unknown, Code: "unreadable", Facts: facts,
			Message: M(msg.DoctorKeyringUnreadable, detail), Hint: M(msg.DoctorKeyringUnreadableHint)}}
	}
	switch st.Kind {
	case keyring.System:
		return []Result{{Status: OK, Code: "system", Facts: facts, Message: M(msg.DoctorKeyringSystem)}}
	case keyring.PlainFile, keyring.Protected:
	default:
		return []Result{{Status: Unknown, Code: "unreadable", Facts: facts,
			Message: M(msg.DoctorKeyringUnreadable, i18n.V("detail", "?")), Hint: M(msg.DoctorKeyringUnreadableHint)}}
	}

	shown := i18n.V("path", env.show(st.Dir))
	var out []Result
	for _, p := range st.Insecure {
		pv := i18n.V("path", env.show(p))
		out = append(out, Result{Status: Fail, Code: "too_open", Path: p, Facts: facts,
			Message: M(msg.DoctorKeyringTooOpen, pv), Hint: M(msg.DoctorHomeChmodHint, pv, i18n.V("mode", octal(home.FilePerm)))})
	}
	file := Result{Status: Warn, Code: "plain_file", Path: st.Dir, Facts: facts,
		Message: M(msg.DoctorKeyringPlainFile, shown), Hint: M(msg.DoctorKeyringFileHint)}
	if st.Kind == keyring.Protected {
		file.Code, file.Message = "protected_file", M(msg.DoctorKeyringProtectedFile, shown)
	}
	if st.Reason != nil {
		file.Details = []Message{M(msg.DoctorKeyringReason, i18n.V("detail", st.Reason.Error()))}
	}
	return append(out, file)
}

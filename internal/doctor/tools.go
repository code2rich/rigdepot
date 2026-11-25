// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package doctor

import (
	"context"
	"strconv"
	"strings"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
	"github.com/code2rich/rigdepot/internal/localconfig"
)

// toolsCheck: what each adapter detects on this computer, one result per tool. A tool whose
// detection could not read what it needed is Unknown and left alone by sync. An installed tool
// whose support was never checked against the real tool, or was checked against an older major
// version, is pointed out.
type toolsCheck struct{}

func (toolsCheck) ID() string { return "tools" }

func (toolsCheck) Title() string { return msg.DoctorToolsTitle }

func (toolsCheck) Run(ctx context.Context, env *Env) []Result {
	detected := env.Detections(ctx)
	if len(detected) == 0 {
		return []Result{{Status: Warn, Code: "no_adapters", Message: M(msg.DoctorToolsNone)}}
	}
	var out []Result
	for _, d := range detected {
		p := d.Adapter.Profile()
		name := p.Name
		if name == "" {
			name = d.Adapter.ID()
		}
		tool := i18n.V("tool", name)
		res := Result{Subject: d.Adapter.ID(), Facts: map[string]string{}}
		switch {
		case d.Disabled:
			res.Status, res.Code = Skipped, "disabled"
			res.Message = M(msg.DoctorToolsDisabled, tool, i18n.V("key", localconfig.KeyToolsDisabled))
		case d.Err != nil:
			res.Status, res.Code = Unknown, "detect_failed"
			res.Message = M(msg.DoctorToolsFailed, tool, i18n.V("detail", d.Err.Error()))
			res.Hint = M(msg.DoctorToolsUnknownHint, tool)
		case d.Detection.Status == adapter.NotInstalled:
			res.Status, res.Code = Skipped, "not_installed"
			res.Message = M(msg.DoctorToolsNotInstalled, tool)
		case d.Detection.Status == adapter.Installed:
			res.Status, res.Code = OK, "installed"
			res.Message = M(msg.DoctorToolsInstalled, tool, i18n.V("version", d.Detection.Version))
			if d.Detection.Version == "" {
				res.Message = M(msg.DoctorToolsInstalledNoVersion, tool)
			}
			switch {
			case p.Verified == nil:
				res.Status, res.Code = Warn, "unverified"
				res.Message = M(msg.DoctorToolsUnverified, tool)
			case newerMajor(d.Detection.Version, p.Verified.Version):
				res.Status, res.Code = Warn, "newer_than_verified"
				res.Message = M(msg.DoctorToolsNewer, tool,
					i18n.V("version", d.Detection.Version), i18n.V("verified", p.Verified.Version))
				res.Hint = M(msg.DoctorToolsNewerHint)
			}
		default:
			res.Status, res.Code = Unknown, "unknown"
			res.Message = M(msg.DoctorToolsUnknown, tool)
			res.Hint = M(msg.DoctorToolsUnknownHint, tool)
			if d.Detection.Reason != "" {
				res.Details = []Message{M(d.Detection.Reason)}
			}
		}
		res.Facts["status"] = d.Detection.Status.String()
		if d.Disabled {
			res.Facts["status"] = "disabled"
		}
		if d.Detection.Version != "" {
			res.Facts["version"] = d.Detection.Version
		}
		if p.Verified != nil {
			res.Facts["verified"] = p.Verified.Version
		}
		out = append(out, res)
	}
	return out
}

// newerMajor reports whether version has a higher major version than verified. Versions that
// do not start with a number are not compared.
func newerMajor(version, verified string) bool {
	a, okA := major(version)
	b, okB := major(verified)
	return okA && okB && a > b
}

func major(v string) (int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	end := strings.IndexFunc(v, func(r rune) bool { return r < '0' || r > '9' })
	if end < 0 {
		end = len(v)
	}
	n, err := strconv.Atoi(v[:end])
	return n, err == nil
}

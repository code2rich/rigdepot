// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/doctor"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
	"github.com/code2rich/rigdepot/internal/localconfig"
)

func init() { register("", newDoctorCmd) }

// newDoctorCmd: rgd doctor. It runs every self-check and reports. It works when the
// configuration or the manifest cannot be read (that is what it is for) and needs no server.
// It exits with 1 when a check failed or could not be done, and 0 otherwise.
func newDoctorCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: a.p.T(msg.DoctorShort),
		Args:  a.noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			report := doctor.Run(cmd.Context(), a.doctorEnv(), doctor.Checks())
			doc := newDoctorDoc(a.p, report)
			if err := a.out.Result(cliout.Schema("doctor", 1), doc, func(w io.Writer) error {
				return a.writeDoctor(w, doc)
			}); err != nil {
				return err
			}
			if w := report.Worst(); w == doctor.Fail || w == doctor.Unknown {
				return cliout.Status(cliout.ExitFailure)
			}
			return nil
		},
	}
}

// doctorEnv describes this computer to the checks. It takes the configuration as far as it
// could be read: the config check reports what could not.
func (a *app) doctorEnv() *doctor.Env {
	env := &doctor.Env{
		GOOS:          a.env.GOOS,
		Getenv:        a.env.Getenv,
		Git:           a.cfg.String(localconfig.KeyGit),
		Server:        a.cfg.String(localconfig.KeyServer),
		DisabledTools: a.cfg.List(localconfig.KeyToolsDisabled),
		Adapters:      adapter.All(),
		ShowPath:      a.showPath,
	}
	if a.home == "" {
		env.HomeErr = errNoHome
	} else {
		env.Home, env.HomeErr = home.At(a.home)
	}
	if h, err := a.env.HomeDir(); err == nil {
		env.UserHome = h
	}
	env.AdapterEnv = doctor.HostAdapterEnv(a.env.GOOS, env.UserHome, a.env.Getenv)
	return env
}

// doctorDoc is the JSON output of rgd doctor (schema rgd.doctor/v1).
type doctorDoc struct {
	Results []doctorResultDoc `json:"results"`
	Summary map[string]int    `json:"summary"`
}

type doctorResultDoc struct {
	Check   string            `json:"check"`
	Subject string            `json:"subject,omitempty"`
	Status  doctor.Status     `json:"status"`
	Code    string            `json:"code"`
	Title   string            `json:"title"`
	Message string            `json:"message"`
	Hint    string            `json:"hint,omitempty"`
	Details []string          `json:"details,omitempty"`
	Path    string            `json:"path,omitempty"`
	Facts   map[string]string `json:"facts,omitempty"`
}

func newDoctorDoc(p *i18n.Printer, r doctor.Report) doctorDoc {
	doc := doctorDoc{Results: []doctorResultDoc{}, Summary: map[string]int{}}
	for _, s := range []doctor.Status{doctor.OK, doctor.Warn, doctor.Fail, doctor.Unknown, doctor.Skipped} {
		doc.Summary[s.String()] = r.Count(s)
	}
	for _, res := range r.Results {
		d := doctorResultDoc{
			Check:   res.Check,
			Subject: res.Subject,
			Status:  res.Status,
			Code:    res.Code,
			Title:   p.T(res.Title),
			Message: res.Message.Text(p),
			Hint:    res.Hint.Text(p),
			Path:    res.Path,
			Facts:   res.Facts,
		}
		for _, m := range res.Details {
			d.Details = append(d.Details, m.Text(p))
		}
		doc.Results = append(doc.Results, d)
	}
	return doc
}

// doctorMarks are the marks in front of each line of the human output. They are ASCII, so that
// they show in every terminal.
var doctorMarks = map[doctor.Status]struct {
	mark  string
	style cliout.Style
}{
	doctor.OK:      {"ok", cliout.Green},
	doctor.Skipped: {"-", cliout.Dim},
	doctor.Warn:    {"!", cliout.Yellow},
	doctor.Unknown: {"?", cliout.Yellow},
	doctor.Fail:    {"x", cliout.Red},
}

// writeDoctor writes the human output: one line per result under the check's title, the hint
// and the details indented below it, and a line of totals.
func (a *app) writeDoctor(w io.Writer, doc doctorDoc) error {
	width := 0
	for _, r := range doc.Results {
		width = max(width, cliout.DisplayWidth(r.Title))
	}
	indent := strings.Repeat(" ", 2+2+1+width+2)
	var b strings.Builder
	last := ""
	for _, r := range doc.Results {
		title := r.Title
		if r.Check == last {
			title = "" // several results of one check: the title once
		}
		last = r.Check
		m := doctorMarks[r.Status]
		fmt.Fprintf(&b, "  %s %s  %s\n", a.out.Paint(m.style, cliout.PadRight(m.mark, 2)), cliout.PadRight(title, width), r.Message)
		for _, d := range r.Details {
			fmt.Fprintf(&b, "%s  %s\n", indent, d)
		}
		if r.Hint != "" {
			fmt.Fprintf(&b, "%s%s\n", indent, a.out.Paint(cliout.Dim, r.Hint))
		}
	}
	b.WriteString("\n  " + a.p.T(msg.DoctorSummary,
		i18n.V("ok", doc.Summary[doctor.OK.String()]),
		i18n.V("warn", doc.Summary[doctor.Warn.String()]),
		i18n.V("fail", doc.Summary[doctor.Fail.String()]),
		i18n.V("unknown", doc.Summary[doctor.Unknown.String()])) + "\n")
	_, err := io.WriteString(w, b.String())
	return err
}

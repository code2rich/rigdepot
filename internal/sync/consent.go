// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package sync

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"time"

	"github.com/code2rich/rigdepot/internal/home"
)

// ConsentFile records which global configuration files the person agreed to have changed,
// relative to the local data directory.
const ConsentFile = home.StateDir + "/consent.json"

const consentVersion = 1

// Consent is one global configuration file a plan changes.
type Consent struct {
	Tool string `json:"tool"`
	// File is the configuration file, written "~/…" or absolute.
	File string `json:"file"`
	// At is when the person agreed; zero when they have not.
	At time.Time `json:"at"`
	// Granted says the person agreed in an earlier run; it is not asked again.
	Granted bool `json:"-"`
}

func (c Consent) key() string { return c.Tool + "\x00" + c.File }

type consentDoc struct {
	Schema int       `json:"schema"`
	Files  []Consent `json:"files"`
}

// errConsentUnknown: the consent file is there but cannot be used. Nothing counts as agreed to,
// and the file is not overwritten.
var errConsentUnknown = errors.New("the record of consents cannot be read")

func (e *Engine) readConsents() (map[string]Consent, error) {
	p := e.data.Join(ConsentFile)
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]Consent{}, nil
	}
	var doc consentDoc
	if err == nil {
		err = json.Unmarshal(data, &doc)
	}
	if err == nil && (doc.Schema < 1 || doc.Schema > consentVersion) {
		err = fmt.Errorf("format %d, this program reads %d", doc.Schema, consentVersion)
	}
	if err != nil {
		return map[string]Consent{}, fmt.Errorf("%w: %s: %v", errConsentUnknown, p, err)
	}
	out := map[string]Consent{}
	for _, c := range doc.Files {
		c.Granted = true
		out[c.key()] = c
	}
	return out, nil
}

// remember adds consents to the file. A file that cannot be read is left as it is: the consents
// count for this run only.
func (e *Engine) remember(cs []Consent) error {
	if len(cs) == 0 {
		return nil
	}
	known, err := e.readConsents()
	if err != nil {
		return err
	}
	for _, c := range cs {
		if _, ok := known[c.key()]; !ok {
			known[c.key()] = c
		}
	}
	doc := consentDoc{Schema: consentVersion}
	for _, c := range known {
		doc.Files = append(doc.Files, c)
	}
	slices.SortFunc(doc.Files, func(a, b Consent) int {
		if a.Tool != b.Tool {
			return cmp.Compare(a.Tool, b.Tool)
		}
		return cmp.Compare(a.File, b.File)
	})
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if _, err := e.data.EnsureSub(home.StateDir); err != nil {
		return err
	}
	return home.WriteFileAtomic(e.data.Join(ConsentFile), append(data, '\n'), home.FilePerm)
}

// consents fills Plan.Consents: every global configuration file the plan changes, and whether
// the person agreed before. A consent file that cannot be read counts as no consent at all.
func (e *Engine) consents(p *Plan) error {
	known, _ := e.readConsents()
	seen := map[string]bool{}
	for _, it := range p.Items {
		if !it.Global || !it.Action.Changes() {
			continue
		}
		c := Consent{Tool: it.Tool, File: it.Target.Path}
		if seen[c.key()] {
			continue
		}
		seen[c.key()] = true
		if k, ok := known[c.key()]; ok {
			c = k
		}
		p.Consents = append(p.Consents, c)
	}
	return nil
}

// ConsentInput is how the command line was run, as far as consent goes.
type ConsentInput struct {
	// SkipGlobal (--skip-global): change no tool's global configuration at all, not even one
	// agreed to before.
	SkipGlobal bool
	// YesGlobal (--yes-global, only ever from the command line): agree to every global change in
	// this plan.
	YesGlobal bool
	// Interactive: a person is at a terminal and --non-interactive was not given.
	Interactive bool
	// Ask asks the person about one file and returns their answer. It is called only when
	// Interactive is set and the file was not agreed to before.
	Ask func(Consent) bool
}

// Approval is what the person agreed to for one plan.
type Approval struct {
	all      bool
	files    map[string]bool
	remember []Consent
}

// Approved reports whether an item may be carried out. Items that change no global
// configuration always may.
func (a Approval) Approved(it Item) bool {
	if !it.Global || a.all {
		return true
	}
	return a.files[Consent{Tool: it.Tool, File: it.Target.Path}.key()]
}

// Approve applies the consent rule to a plan:
//
//   - an uninstall needs no consent: the person asked for exactly that;
//   - --skip-global agrees to nothing;
//   - a file agreed to in an earlier run is agreed to;
//   - --yes-global agrees to every file of this plan, and so does a person at a terminal who
//     answers yes; both are remembered;
//   - otherwise (no terminal, no --yes-global) nothing is agreed to, and the global changes wait.
func (p *Plan) Approve(in ConsentInput, now time.Time) Approval {
	ap := Approval{files: map[string]bool{}}
	if p.Uninstall {
		ap.all = true
		return ap
	}
	if in.SkipGlobal {
		return ap
	}
	for _, c := range p.Consents {
		yes := c.Granted || in.YesGlobal || (in.Interactive && in.Ask != nil && in.Ask(c))
		if !yes {
			continue
		}
		ap.files[c.key()] = true
		if !c.Granted {
			c.At = now.UTC()
			ap.remember = append(ap.remember, c)
		}
	}
	return ap
}

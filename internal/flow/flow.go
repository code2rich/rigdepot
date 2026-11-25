// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package flow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/desired"
	"github.com/code2rich/rigdepot/internal/home"
)

// formatVersion is the version of the files this package writes.
const formatVersion = 1

var (
	// ErrUnknownFlow: no workflow with this code has been synced to this computer.
	ErrUnknownFlow = errors.New("no such workflow on this computer")
	// ErrUnreadable: a file of this package could not be read or parsed; what it holds is
	// unknown.
	ErrUnreadable = errors.New("the workflow's files cannot be read")
	// ErrChanged: the workflow was changed by a sync while a run was under way.
	ErrChanged = errors.New("the workflow changed since this run started")
)

// Definition is a workflow as kept on this computer.
type Definition struct {
	Schema int `json:"schema"`
	// Code is the workflow's resource code and Name its display name.
	Code string `json:"code"`
	Name string `json:"name"`
	// Digest is the content digest of the workflow as synced. A run remembers it.
	Digest string `json:"digest"`
	Steps  []Step `json:"steps"`
}

// Step is one step, with what the sync knew about the resources it names.
type Step struct {
	Use    Ref      `json:"use"`
	With   []Ref    `json:"with,omitempty"`
	Checks []Check  `json:"checks,omitempty"`
	Forbid []string `json:"forbid,omitempty"`
	// Then is kind.ThenContinue or kind.ThenConfirm.
	Then string `json:"then"`
}

// Ref is a resource a step names.
type Ref struct {
	Code string `json:"code"`
	// Kind and Name are empty when the repository did not have the resource readable.
	Kind string `json:"kind,omitempty"`
	Name string `json:"name,omitempty"`
	// Delivered says the resource was delivered to this computer by the same sync; one that is
	// not (an optional resource nobody turned on) cannot be used until it is.
	Delivered bool `json:"delivered"`
}

// Check is one check of a step (kind.Check).
type Check struct {
	Text string `json:"text"`
	By   string `json:"by"`
	Run  string `json:"run,omitempty"`
}

// Kept says what Keep did.
type Kept struct {
	// Written and Removed are workflow codes, sorted.
	Written, Removed []string
}

// Keep writes the workflows of a desired state to flows/<code>.json, so that rgd flow next
// finds them without the repository, and removes the files of workflows the state no longer
// delivers. The file of a workflow that could not be read this time stays as it was: its state
// is unknown, not gone.
func Keep(data home.Dir, st *desired.State) (Kept, error) {
	var out Kept
	dir := data.Join(home.FlowsDir)
	if !slices.ContainsFunc(st.Resources, isFlow) {
		// Nothing to write: only what is there already may have to go.
		if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
	} else if _, err := data.EnsureSub(home.FlowsDir); err != nil {
		return out, err
	}
	known := map[string]desired.Resource{}
	for _, r := range st.Resources {
		known[r.Ref.Code] = r
	}
	ref := func(code string) Ref {
		r := Ref{Code: code}
		if res, ok := known[code]; ok {
			r.Kind, r.Name, r.Delivered = res.Ref.Kind, res.Meta.Name, true
			return r
		}
		for _, x := range st.Excluded {
			if x.Ref.Code == code {
				r.Kind, r.Name = x.Ref.Kind, x.Meta.Name
			}
		}
		return r
	}
	keep := map[string]bool{}
	for _, f := range st.Failed {
		keep[f.Ref.Code] = true
	}
	for _, r := range st.Resources {
		fl, ok := r.Spec.(kind.Flow)
		if !ok {
			continue
		}
		def := Definition{Schema: formatVersion, Code: r.Ref.Code, Name: r.Meta.Name, Digest: string(r.Digest)}
		for _, s := range fl.Steps() {
			step := Step{Use: ref(s.Use), Forbid: s.Forbid, Then: s.Then}
			for _, w := range s.With {
				step.With = append(step.With, ref(w))
			}
			for _, c := range s.Checks {
				step.Checks = append(step.Checks, Check{Text: c.Text, By: c.By, Run: c.Run})
			}
			def.Steps = append(def.Steps, step)
		}
		data, err := json.MarshalIndent(def, "", "  ")
		if err != nil {
			return out, err
		}
		if err := home.WriteFileAtomic(filepath.Join(dir, r.Ref.Code+".json"), append(data, '\n'), home.FilePerm); err != nil {
			return out, err
		}
		keep[r.Ref.Code] = true
		out.Written = append(out.Written, r.Ref.Code)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out, err
	}
	for _, e := range entries {
		code, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() || keep[code] || !resource.ValidCode(code) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return out, err
		}
		out.Removed = append(out.Removed, code)
	}
	slices.Sort(out.Written)
	slices.Sort(out.Removed)
	return out, nil
}

func isFlow(r desired.Resource) bool {
	_, ok := r.Spec.(kind.Flow)
	return ok
}

// Load reads the workflow with the given code as last synced.
func Load(data home.Dir, code string) (*Definition, error) {
	if !resource.ValidCode(code) {
		return nil, fmt.Errorf("%w: %q", ErrUnknownFlow, code)
	}
	p := data.Join(home.FlowsDir, code+".json")
	raw, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrUnknownFlow, code)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrUnreadable, p, err)
	}
	var def Definition
	if err := json.Unmarshal(raw, &def); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrUnreadable, p, err)
	}
	if def.Schema < 1 || def.Schema > formatVersion || def.Code != code || len(def.Steps) == 0 {
		return nil, fmt.Errorf("%w: %s: not a workflow this program can read", ErrUnreadable, p)
	}
	return &def, nil
}

// runFile is where the run of a workflow in a directory is kept. The directory is part of the
// name through a digest, so that names stay short and carry nothing of the path.
func runFile(data home.Dir, code, dir string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(dir)))
	return data.Join(home.FlowsDir, "runs", code+"-"+hex.EncodeToString(sum[:6])+".json")
}

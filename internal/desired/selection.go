// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package desired

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"

	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/home"
)

// SelectionFile is where the selection is kept, relative to the local data directory.
const SelectionFile = home.StateDir + "/selection.json"

// selectionVersion is the format of SelectionFile this program reads and writes.
const selectionVersion = 1

// ErrSelectionUnknown means the selection file exists but cannot be read, does not parse, or has
// a newer format. What the person enabled and disabled is then unknown, and a sync must not go
// ahead: it would install what they turned off and remove what they turned on.
var ErrSelectionUnknown = errors.New("the local selection of resources cannot be read")

// ErrRequired is returned by Disable for a required resource.
var ErrRequired = errors.New("a required resource cannot be disabled")

// Selection is what the person enabled and disabled on this computer, when there is no server to
// keep it: optional resources they turned on and recommended ones they turned off, by code.
type Selection struct {
	Enabled  []string `json:"enabled,omitempty"`
	Disabled []string `json:"disabled,omitempty"`
}

type selectionFile struct {
	Schema int `json:"schema"`
	Selection
}

// ReadSelection reads the selection. A missing file is an empty selection; any other failure is
// an error that satisfies errors.Is(err, ErrSelectionUnknown).
func ReadSelection(dir home.Dir) (Selection, error) {
	p := dir.Join(SelectionFile)
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return Selection{}, nil
	}
	if err != nil {
		return Selection{}, fmt.Errorf("%w: %s: %v", ErrSelectionUnknown, p, err)
	}
	var f selectionFile
	if err := json.Unmarshal(data, &f); err != nil {
		return Selection{}, fmt.Errorf("%w: %s: %v", ErrSelectionUnknown, p, err)
	}
	if f.Schema < 1 || f.Schema > selectionVersion {
		return Selection{}, fmt.Errorf("%w: %s: format %d, this program reads %d", ErrSelectionUnknown, p, f.Schema, selectionVersion)
	}
	return f.Selection, nil
}

// WriteSelection replaces the selection file, atomically.
func WriteSelection(dir home.Dir, s Selection) error {
	if _, err := dir.EnsureSub(home.StateDir); err != nil {
		return err
	}
	slices.Sort(s.Enabled)
	slices.Sort(s.Disabled)
	data, err := json.MarshalIndent(selectionFile{Schema: selectionVersion, Selection: Selection{
		Enabled: slices.Compact(s.Enabled), Disabled: slices.Compact(s.Disabled),
	}}, "", "  ")
	if err != nil {
		return err
	}
	return home.WriteFileAtomic(dir.Join(SelectionFile), append(data, '\n'), home.FilePerm)
}

// Enable records that the person wants the resource: an optional resource is turned on, a
// recommended one they had turned off is turned on again. It reports whether the selection
// changed; enabling a required resource changes nothing.
func (s *Selection) Enable(code string, p resource.Policy) bool {
	changed := remove(&s.Disabled, code)
	if p == resource.PolicyOptional && !slices.Contains(s.Enabled, code) {
		s.Enabled = append(s.Enabled, code)
		changed = true
	}
	return changed
}

// Disable records that the person does not want the resource. It fails with ErrRequired for a
// required resource, and reports whether the selection changed.
func (s *Selection) Disable(code string, p resource.Policy) (bool, error) {
	if p == resource.PolicyRequired {
		return false, ErrRequired
	}
	changed := remove(&s.Enabled, code)
	if p != resource.PolicyOptional && !slices.Contains(s.Disabled, code) {
		s.Disabled = append(s.Disabled, code)
		changed = true
	}
	return changed, nil
}

func remove(list *[]string, code string) bool {
	n := len(*list)
	*list = slices.DeleteFunc(*list, func(c string) bool { return c == code })
	return len(*list) != n
}

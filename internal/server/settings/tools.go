// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package settings

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// The organization-wide switches of the tool adapters and the minimum command-line version: the
// "tool adapters" page. Where each tool keeps what comes from the adapters' built-in profiles,
// not from here.

// The actions of this file, also the audit's.
const (
	ActionAdapterEnable   = "adapter.enable"
	ActionAdapterDisable  = "adapter.disable"
	ActionCLIPolicyUpdate = "cli.policy_update"
)

const (
	// keyAdaptersDisabled lists the adapters the organization turned off. Off rather than on,
	// so that a tool added by a newer version is on, like every other, until someone turns it
	// off.
	keyAdaptersDisabled = "adapters.disabled"
	keyCLIMinVersion    = "cli.min_version"
)

var cliPolicyObject = audit.Object{Kind: "settings", ID: "cli", Label: "Command-line versions"}

// Tools are the tool settings as administrators see them.
type Tools struct {
	DisabledAdapters []string // sorted
	// CLIMinVersion is the minimum command-line version an administrator set, "" when none:
	// then the server's own floor applies.
	CLIMinVersion string
}

// Tools returns the tool settings. Platform administrators only.
func (s *Service) Tools(ctx context.Context, actor audit.Actor) (Tools, error) {
	if err := s.authorize(ctx, actor, ActionSettingsRead, platformObject); err != nil {
		return Tools{}, err
	}
	var t Tools
	var err error
	if t.DisabledAdapters, err = disabledAdapters(ctx, s.db); err != nil {
		return Tools{}, err
	}
	if t.CLIMinVersion, err = s.CLIMinVersion(ctx); err != nil {
		return Tools{}, err
	}
	return t, nil
}

// DisabledAdapters returns the IDs of the adapters the organization turned off, sorted. There is
// no permission check: what every computer receives depends on it.
func (s *Service) DisabledAdapters(ctx context.Context) ([]string, error) {
	return disabledAdapters(ctx, s.db)
}

func disabledAdapters(ctx context.Context, q dbx.Querier) ([]string, error) {
	var ids []string
	if _, err := getJSON(ctx, q, keyAdaptersDisabled, &ids); err != nil {
		return nil, err
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

// CLIMinVersion returns the minimum command-line version an administrator set, "" when none.
// There is no permission check: every computer is told it.
func (s *Service) CLIMinVersion(ctx context.Context) (string, error) {
	var v string
	if _, err := getJSON(ctx, s.db, keyCLIMinVersion, &v); err != nil {
		return "", err
	}
	return v, nil
}

// SetAdapterEnabled turns a tool on or off for the whole organization and records it. label is
// the tool's name, for the audit entry. Platform administrators only.
func (s *Service) SetAdapterEnabled(ctx context.Context, actor audit.Actor, id, label string, enabled bool) error {
	action := ActionAdapterDisable
	if enabled {
		action = ActionAdapterEnable
	}
	object := audit.Object{Kind: "adapter", ID: id, Label: label}
	if err := s.authorize(ctx, actor, action, object); err != nil {
		return err
	}
	return s.db.InTx(ctx, func(tx *dbx.Tx) error {
		if err := tx.Lock(ctx, lockKey); err != nil {
			return err
		}
		ids, err := disabledAdapters(ctx, tx)
		if err != nil {
			return err
		}
		if slices.Contains(ids, id) != enabled {
			return nil // already so: nothing changed, nothing to record
		}
		if enabled {
			ids = slices.DeleteFunc(ids, func(m string) bool { return m == id })
		} else {
			ids = append(ids, id)
			slices.Sort(ids)
		}
		if ids == nil {
			ids = []string{}
		}
		if err := putJSON(ctx, tx, keyAdaptersDisabled, ids, actor, s.now()); err != nil {
			return err
		}
		_, err = s.log.Append(ctx, tx, audit.Entry{Actor: actor, Action: action, Object: object})
		return err
	})
}

// BelowFloorError refuses a minimum command-line version below what the server supports.
type BelowFloorError struct{ Floor string }

func (e *BelowFloorError) Error() string {
	return "settings: the minimum command-line version cannot be below " + e.Floor
}

// SetCLIMinVersion sets the minimum command-line version and records the change. floor is the
// lowest version this server supports; a version below it is refused with *BelowFloorError.
// Platform administrators only.
func (s *Service) SetCLIMinVersion(ctx context.Context, actor audit.Actor, version, floor string) error {
	if err := s.authorize(ctx, actor, ActionCLIPolicyUpdate, cliPolicyObject); err != nil {
		return err
	}
	if _, ok := ParseVersion(version); !ok {
		return &ValidationError{Field: "min_version", Code: "invalid"}
	}
	if CompareVersions(version, floor) < 0 {
		return &BelowFloorError{Floor: floor}
	}
	return s.db.InTx(ctx, func(tx *dbx.Tx) error {
		if err := tx.Lock(ctx, lockKey); err != nil {
			return err
		}
		var old string
		if _, err := getJSON(ctx, tx, keyCLIMinVersion, &old); err != nil {
			return err
		}
		if old == version {
			return nil
		}
		if err := putJSON(ctx, tx, keyCLIMinVersion, version, actor, s.now()); err != nil {
			return err
		}
		_, err := s.log.Append(ctx, tx, audit.Entry{Actor: actor, Action: ActionCLIPolicyUpdate, Object: cliPolicyObject,
			Change: map[string]any{"min_version": map[string]any{"from": old, "to": version}}})
		return err
	})
}

// Version is a parsed version number: MAJOR.MINOR.PATCH and an optional pre-release suffix.
type Version struct {
	Major, Minor, Patch int
	Pre                 string
}

func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// ParseVersion reads "1.2.3" or "1.2.3-rc.1", the contract's SemVer; build metadata ("+…") is
// dropped.
func ParseVersion(s string) (Version, bool) {
	s, _, _ = strings.Cut(s, "+")
	core, pre, hasPre := strings.Cut(s, "-")
	if hasPre && pre == "" {
		return Version{}, false
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, false
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 || p != strconv.Itoa(v) {
			return Version{}, false
		}
		n[i] = v
	}
	for _, c := range pre {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '.' || c == '-') {
			return Version{}, false
		}
	}
	return Version{Major: n[0], Minor: n[1], Patch: n[2], Pre: pre}, true
}

// CompareVersions orders two versions: -1, 0 or 1. A pre-release comes before its release;
// pre-releases of one release are compared as text. A version that does not parse comes before
// every one that does.
func CompareVersions(a, b string) int {
	va, okA := ParseVersion(a)
	vb, okB := ParseVersion(b)
	switch {
	case !okA && !okB:
		return 0
	case !okA:
		return -1
	case !okB:
		return 1
	}
	for _, d := range [][2]int{{va.Major, vb.Major}, {va.Minor, vb.Minor}, {va.Patch, vb.Patch}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case va.Pre == vb.Pre:
		return 0
	case va.Pre == "":
		return 1
	case vb.Pre == "":
		return -1
	}
	return strings.Compare(va.Pre, vb.Pre)
}

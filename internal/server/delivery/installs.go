// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package delivery

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// liveDevices returns the computers that count: device id → user id. Without Options.LiveDevices
// that is every computer that reported.
func (s *Service) liveDevices(ctx context.Context) (map[string]string, error) {
	if s.live != nil {
		return s.live(ctx)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT device_id, user_id FROM device_syncs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var d, u string
		if err := rows.Scan(&d, &u); err != nil {
			return nil, err
		}
		out[d] = u
	}
	return out, rows.Err()
}

// installedRow is one resource installed in one tool on one computer.
type installedRow struct {
	device, resource string
}

// installed returns the installs with status installed on the computers that count; resourceID
// "" for every resource.
func (s *Service) installed(ctx context.Context, resourceID string) ([]installedRow, map[string]string, error) {
	live, err := s.liveDevices(ctx)
	if err != nil {
		return nil, nil, err
	}
	q := `SELECT device_id, resource_id FROM installs WHERE status = 'installed'`
	var args []any
	if resourceID != "" {
		q += ` AND resource_id = ?`
		args = append(args, resourceID)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []installedRow
	for rows.Next() {
		var r installedRow
		if err := rows.Scan(&r.device, &r.resource); err != nil {
			return nil, nil, err
		}
		if _, ok := live[r.device]; ok {
			out = append(out, r)
		}
	}
	return out, live, rows.Err()
}

// InstalledPeople returns, by resource id, how many people have it installed on at least one
// computer that is signed in.
func (s *Service) InstalledPeople(ctx context.Context) (map[string]int, error) {
	rows, live, err := s.installed(ctx, "")
	if err != nil {
		return nil, err
	}
	people := map[string]map[string]bool{}
	for _, r := range rows {
		if people[r.resource] == nil {
			people[r.resource] = map[string]bool{}
		}
		people[r.resource][live[r.device]] = true
	}
	out := make(map[string]int, len(people))
	for id, set := range people {
		out[id] = len(set)
	}
	return out, nil
}

// Installed returns, for one resource and by user id, on how many signed-in computers each
// person has it.
func (s *Service) Installed(ctx context.Context, resourceID string) (map[string]int, error) {
	rows, live, err := s.installed(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	devices := map[string]map[string]bool{}
	for _, r := range rows {
		u := live[r.device]
		if devices[u] == nil {
			devices[u] = map[string]bool{}
		}
		devices[u][r.device] = true
	}
	out := make(map[string]int, len(devices))
	for u, set := range devices {
		out[u] = len(set)
	}
	return out, nil
}

// Install is a resource in one tool on one of a person's computers, as last reported.
type Install struct {
	DeviceID string
	Tool     string
	Version  int
	Status   string
	Degraded bool
	Form     string
}

// MyInstalls returns where a resource is on the person's signed-in computers, per tool: every
// install except the removed ones, by device and tool.
func (s *Service) MyInstalls(ctx context.Context, userID, resourceID string) ([]Install, error) {
	live, err := s.liveDevices(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT i.device_id, i.tool, i.version, i.status, i.degraded, i.form
		FROM installs i JOIN device_syncs d ON d.device_id = i.device_id
		WHERE d.user_id = ? AND i.resource_id = ? AND i.status <> 'removed'
		ORDER BY i.device_id, i.tool`, userID, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Install{}
	for rows.Next() {
		var in Install
		if err := rows.Scan(&in.DeviceID, &in.Tool, &in.Version, &in.Status, &in.Degraded, &in.Form); err != nil {
			return nil, err
		}
		if live[in.DeviceID] == userID {
			out = append(out, in)
		}
	}
	return out, rows.Err()
}

// DeviceSync is what a computer said at its last sync.
type DeviceSync struct {
	At         time.Time // when the sync finished, by the computer's clock
	Result     string
	CLIVersion string
	Tools      []DetectedTool
}

// DeviceSyncs returns the last sync of each of the devices that ever reported, by device id.
func (s *Service) DeviceSyncs(ctx context.Context, deviceIDs []string) (map[string]DeviceSync, error) {
	out := map[string]DeviceSync{}
	if len(deviceIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(deviceIDs))
	for i, id := range deviceIDs {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `SELECT device_id, finished_at, result, cli_version, tools FROM device_syncs
		WHERE device_id IN (`+placeholders(len(deviceIDs))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, tools string
		var at int64
		var d DeviceSync
		if err := rows.Scan(&id, &at, &d.Result, &d.CLIVersion, &tools); err != nil {
			return nil, err
		}
		d.At = dbx.Time(at)
		if err := json.Unmarshal([]byte(tools), &d.Tools); err != nil {
			return nil, fmt.Errorf("delivery: tools of device %s: %w", id, err)
		}
		out[id] = d
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	b := make([]byte, 0, 2*n)
	for i := range n {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '?')
	}
	return string(b)
}

// Usage is how far a group of people has connected: the figures of the home page and the tool
// adapters page.
type Usage struct {
	// People have at least one signed-in computer; Devices counts those computers.
	People  int
	Devices int
	// RequiredComplete counts the People who have every required resource of their scope
	// installed (in some tool, on some computer).
	RequiredComplete int
	// ByTool counts the people with each tool found at the last sync of one of their computers,
	// by adapter id.
	ByTool map[string]int
	// CLIVersions counts the people by the command-line version of their most recent sync.
	CLIVersions map[string]int
}

// UsageOf counts the usage of the people in users; nil means everyone.
func (s *Service) UsageOf(ctx context.Context, users []string) (Usage, error) {
	u := Usage{ByTool: map[string]int{}, CLIVersions: map[string]int{}}
	live, err := s.liveDevices(ctx)
	if err != nil {
		return u, err
	}
	var in map[string]bool
	if users != nil {
		in = make(map[string]bool, len(users))
		for _, id := range users {
			in[id] = true
		}
	}
	devicesOf := map[string][]string{}
	var ids []string
	for d, user := range live {
		if in != nil && !in[user] {
			continue
		}
		devicesOf[user] = append(devicesOf[user], d)
		ids = append(ids, d)
	}
	u.People, u.Devices = len(devicesOf), len(ids)
	syncs, err := s.DeviceSyncs(ctx, ids)
	if err != nil {
		return u, err
	}
	installed, _, err := s.installed(ctx, "")
	if err != nil {
		return u, err
	}
	has := map[string]map[string]bool{} // user → resource ids installed
	for _, r := range installed {
		user := live[r.device]
		if has[user] == nil {
			has[user] = map[string]bool{}
		}
		has[user][r.resource] = true
	}
	for user, devs := range devicesOf {
		tools := map[string]bool{}
		var latest *DeviceSync
		for _, d := range devs {
			sy, ok := syncs[d]
			if !ok {
				continue
			}
			for _, t := range sy.Tools {
				tools[t.ID] = true
			}
			if latest == nil || sy.At.After(latest.At) {
				latest = &sy
			}
		}
		for t := range tools {
			u.ByTool[t]++
		}
		if latest != nil {
			u.CLIVersions[latest.CLIVersion]++
		}
		complete, err := s.requiredComplete(ctx, user, has[user])
		if err != nil {
			return u, err
		}
		if complete {
			u.RequiredComplete++
		}
	}
	return u, nil
}

// requiredComplete reports whether every required resource the person gets is among has.
func (s *Service) requiredComplete(ctx context.Context, userID string, has map[string]bool) (bool, error) {
	st, err := s.Desired(ctx, access.Actor{UserID: userID, Origin: access.OriginSystem})
	if err != nil {
		return false, err
	}
	for _, it := range st.Items {
		if it.Resource.Policy == resource.PolicyRequired && it.Reason == ReasonPolicy && !has[it.Resource.ID] {
			return false, nil
		}
	}
	return true, nil
}

// Summary is the actor's view of how far people have connected (contract: getInstallsSummary).
// A platform administrator sees everyone; a product's owner sees its members. product narrows
// it to one product; without it, an owner sees the members of every product they own. Anyone
// else is ErrDenied.
func (s *Service) Summary(ctx context.Context, a access.Actor, product string) (Usage, error) {
	if a.UserID == "" {
		return Usage{}, ErrDenied
	}
	products, err := s.access.ListProducts(ctx, a)
	if err != nil {
		return Usage{}, err
	}
	var scope []access.Product
	for _, p := range products {
		if product == "" || p.Code == product {
			scope = append(scope, p)
		}
	}
	if product != "" && len(scope) == 0 {
		return Usage{}, ErrNotFound.with(map[string]any{"product": product})
	}
	if product == "" {
		d := s.access.Can(ctx, a, access.ActionInstallsRead, access.TargetOrg())
		if d.Err != nil {
			return Usage{}, d.Err
		}
		if d.Allowed {
			return s.UsageOf(ctx, nil)
		}
	}
	users := []string{}
	allowed := false
	for _, p := range scope {
		d := s.access.Can(ctx, a, access.ActionInstallsRead, access.TargetProduct(p.ID))
		if d.Err != nil {
			return Usage{}, d.Err
		}
		if !d.Allowed {
			continue
		}
		allowed = true
		members, err := s.access.ListMembers(ctx, a, p.Code)
		if err != nil {
			return Usage{}, err
		}
		for _, m := range members {
			if !slices.Contains(users, m.User.ID) {
				users = append(users, m.User.ID)
			}
		}
	}
	if !allowed {
		return Usage{}, ErrDenied
	}
	return s.UsageOf(ctx, users)
}

// Counts sorts a count by its key, for stable answers.
func Counts(m map[string]int) []KeyCount {
	out := make([]KeyCount, 0, len(m))
	for k, n := range m {
		out = append(out, KeyCount{Key: k, N: n})
	}
	slices.SortFunc(out, func(a, b KeyCount) int {
		if c := cmp.Compare(b.N, a.N); c != 0 {
			return c
		}
		return cmp.Compare(a.Key, b.Key)
	})
	return out
}

// KeyCount is one entry of a count.
type KeyCount struct {
	Key string
	N   int
}

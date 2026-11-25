// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/events"
)

// TestAccessEventsReachTheBus: access publishes on the bus serve makes, after
// its own commit, so that notify and the modules hear of its changes.
func TestAccessEventsReachTheBus(t *testing.T) {
	f := newAPIWith(t, fixtureOptions{background: true})
	var mu sync.Mutex
	var got []string
	f.app.bg.Events.Subscribe("test", nil, func(_ context.Context, ev events.Event) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, ev.Name)
		return nil
	})
	ctx := context.Background()
	admin := access.Actor{UserID: testAdmin, Origin: access.OriginWeb}
	if _, err := f.app.access.CreateProduct(ctx, admin, access.NewProduct{Code: "shop", Name: "Shop", Owners: []string{testMember}}); err != nil {
		t.Fatal(err)
	}
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := f.app.bg.Events.Drain(dctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(got, access.ProductCreated{}.EventName()) {
		t.Errorf("events on the bus: %v", got)
	}
}

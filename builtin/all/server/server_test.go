// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"testing"

	"github.com/code2rich/rigdepot/ext/notify"
	"github.com/code2rich/rigdepot/ext/notify/notifytest"
)

func TestMain(m *testing.M) { notifytest.Main(m) }

// TestChannelsLoad: importing this package registers the channels, and each one meets the
// contract every channel must.
func TestChannelsLoad(t *testing.T) {
	channels := notify.All()
	if len(channels) == 0 {
		t.Fatal("no notification channel is registered")
	}
	for _, ch := range channels {
		for _, problem := range notifytest.Check(ch) {
			t.Errorf("channel %s: %s", ch.ID(), problem)
		}
	}
	if _, ok := notify.Lookup("email"); !ok {
		t.Error("the e-mail channel is not registered")
	}
}

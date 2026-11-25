// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package notifytest is the test suite for notification channels. Version: v0.
//
// A channel's test file starts with
//
//	func TestMain(m *testing.M) { notifytest.Main(m) }
//
//	func TestContract(t *testing.T) { notifytest.Run(t, New()) }
//
// and goes on to send through a local fake server: tests use no network.
//
// Unlike the package it tests, this package depends on the core. It is only ever imported from
// test files.
package notifytest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/ext/internal/registry"
	"github.com/code2rich/rigdepot/ext/notify"
	"github.com/code2rich/rigdepot/internal/testenv"
)

// Main runs the tests of a package in a temporary home with the network limited to this
// machine, like every test in this repository.
func Main(m *testing.M) {
	testenv.Main(m)
}

// Run checks what every channel must do, without sending anything:
//
//   - its ID has the shape every registry requires;
//   - its ConfigSchema is a JSON Schema of an object whose secret fields can be read;
//   - Send with an empty configuration fails with an error, quickly, instead of panicking or
//     sending somewhere by default;
//   - Send with a canceled context fails.
func Run(t *testing.T, ch notify.Channel) {
	t.Helper()
	for _, problem := range Check(ch) {
		t.Error(problem)
	}
}

// Check returns what is wrong with ch, as sentences; empty when nothing is. Run reports them.
func Check(ch notify.Channel) []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }
	if !registry.ValidID(ch.ID()) {
		add("ID %q: use lower-case letters, digits and single hyphens", ch.ID())
	}
	var schema struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(ch.ConfigSchema(), &schema); err != nil || schema.Type != "object" {
		add("ConfigSchema is not a JSON Schema with \"type\": \"object\" (%v)", err)
	} else if _, err := notify.SecretFields(ch.ConfigSchema()); err != nil {
		add("the secret fields of ConfigSchema cannot be read: %v", err)
	}
	msg := notify.Message{To: notify.Recipient{Address: "someone@example.com"}, Subject: "s", Text: "t", Kind: "test", Locale: "en"}
	switch err := send(ch, context.Background(), notify.Config{}, msg); {
	case err == nil:
		add("Send with an empty configuration succeeded; it must fail instead of sending somewhere by default")
	case errors.Is(err, errHung):
		add("Send with an empty configuration %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	switch err := send(ch, canceled, notify.Config{"host": "127.0.0.1", "port": 1, "url": "http://127.0.0.1:1/"}, msg); {
	case err == nil:
		add("Send with a canceled context succeeded")
	case errors.Is(err, errHung):
		add("Send with a canceled context %v", err)
	}
	return out
}

var errHung = errors.New("did not return within 5 seconds of its context ending")

// send calls Send with a time limit and turns a panic into an error.
func send(ch notify.Channel, ctx context.Context, cfg notify.Config, msg notify.Message) (err error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- fmt.Errorf("the channel panicked: %v", p)
			}
		}()
		done <- ch.Send(ctx, cfg, msg)
	}()
	select {
	case err = <-done:
		return err
	case <-time.After(10 * time.Second):
		return errHung
	}
}

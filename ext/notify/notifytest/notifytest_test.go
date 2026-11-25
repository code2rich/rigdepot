// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package notifytest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/ext/notify"
	"github.com/code2rich/rigdepot/internal/testenv"
)

// The structure check looks for testenv.Main in TestMain; Main is the same call.
func TestMain(m *testing.M) { testenv.Main(m) }

type fakeChannel struct {
	id      string
	schema  string
	lenient bool // sends with any configuration
	panics  bool
}

// TestPanicIsAnError: a channel that panics fails like one that returns an error; it does not
// take the test binary down.
func TestPanicIsAnError(t *testing.T) {
	if err := send(fakeChannel{id: "webhook", panics: true}, context.Background(), nil, notify.Message{}); err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Errorf("send = %v", err)
	}
}

func (c fakeChannel) ID() string           { return c.id }
func (c fakeChannel) ConfigSchema() []byte { return []byte(c.schema) }
func (c fakeChannel) Send(ctx context.Context, cfg notify.Config, _ notify.Message) error {
	if c.panics {
		panic("boom")
	}
	if c.lenient {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if cfg.String("url") == "" {
		return errors.New("no url")
	}
	return nil
}

const goodSchema = `{"type":"object","required":["url"],"properties":{"url":{"type":"string"},"token":{"type":"string","writeOnly":true}}}`

func TestGoodChannelPasses(t *testing.T) {
	Run(t, fakeChannel{id: "webhook", schema: goodSchema})
}

func TestCheckFindsProblems(t *testing.T) {
	for name, tc := range map[string]struct {
		ch   fakeChannel
		want string
	}{
		"bad ID":                 {fakeChannel{id: "Web_Hook", schema: goodSchema}, "ID"},
		"schema not JSON":        {fakeChannel{id: "webhook", schema: "{"}, "ConfigSchema"},
		"schema not an object":   {fakeChannel{id: "webhook", schema: `{"type":"string"}`}, "ConfigSchema"},
		"sends without settings": {fakeChannel{id: "webhook", schema: goodSchema, lenient: true}, "empty configuration"},
	} {
		t.Run(name, func(t *testing.T) {
			got := Check(tc.ch)
			if !strings.Contains(strings.Join(got, "\n"), tc.want) {
				t.Errorf("Check = %q, want a problem about %q", got, tc.want)
			}
		})
	}
}

func TestSecretFields(t *testing.T) {
	got, err := notify.SecretFields([]byte(goodSchema))
	if err != nil || len(got) != 1 || got[0] != "token" {
		t.Errorf("SecretFields = %v, %v", got, err)
	}
	if _, err := notify.SecretFields([]byte("not json")); err == nil {
		t.Error("an unreadable schema has no error")
	}
}

func TestConfig(t *testing.T) {
	c, err := notify.ParseConfig([]byte(`{"host":"smtp.example.com","port":465,"tls":true,"ratio":1.5}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.String("host") != "smtp.example.com" || c.Int("port") != 465 || !c.Bool("tls") || c.Int("ratio") != 0 || c.String("port") != "" {
		t.Errorf("Config accessors: %v", c)
	}
	for _, bad := range []string{"null", "[]", "{"} {
		if _, err := notify.ParseConfig([]byte(bad)); err == nil {
			t.Errorf("ParseConfig(%s) has no error", bad)
		}
	}
}

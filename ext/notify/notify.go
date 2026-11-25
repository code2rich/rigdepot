// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
)

// Channel delivers rendered messages: e-mail, a webhook, an instant messenger.
type Channel interface {
	// ID is lower-case letters, digits and single hyphens, such as "email". It never changes
	// once released: saved configurations are keyed by it.
	ID() string
	// ConfigSchema is a JSON Schema (an object) of the channel's settings. Secret fields carry
	// "writeOnly": true.
	ConfigSchema() []byte
	// Send delivers msg with cfg. It returns when the message was handed over (an SMTP server
	// accepted it, a webhook answered 2xx) or failed; the core retries failures later. ctx
	// carries the deadline for the whole attempt.
	Send(ctx context.Context, cfg Config, msg Message) error
}

// Recipient is who a message goes to.
type Recipient struct {
	// Address is where the channel delivers: an e-mail address for e-mail, the person's ID in
	// an instant messenger.
	Address string
	// Name is the display name, possibly empty.
	Name string
}

// Message is one rendered message.
type Message struct {
	To      Recipient
	From    string // display name of the sender: the organization or the product
	Subject string // one line
	Text    string // plain text body
	HTML    string // HTML body; may be empty
	Link    string // where to see the details, absolute; may be empty
	Kind    string // what it is about: an event type such as "resource.published", or "digest", "test"
	Locale  string // the language it is written in, a BCP 47 tag
}

// Config is a channel's saved settings: a JSON object shaped by its ConfigSchema.
type Config map[string]any

// ParseConfig reads a JSON object. Numbers stay exact.
func ParseConfig(data []byte) (Config, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, err
	}
	if c == nil {
		return nil, errors.New("notify: the configuration is not a JSON object")
	}
	return c, nil
}

// String returns the value under key as a string; "" when it is missing or not a string.
func (c Config) String(key string) string {
	s, _ := c[key].(string)
	return s
}

// Int returns the value under key as an integer; 0 when it is missing or not an integer.
func (c Config) Int(key string) int {
	switch v := c[key].(type) {
	case json.Number:
		n, _ := strconv.Atoi(v.String())
		return n
	case float64:
		if v == float64(int(v)) {
			return int(v)
		}
	case int:
		return v
	}
	return 0
}

// Bool returns the value under key as a boolean; false when it is missing or not a boolean.
func (c Config) Bool(key string) bool {
	b, _ := c[key].(bool)
	return b
}

// SecretFields returns the names of the top-level properties a ConfigSchema marks
// "writeOnly": true. A schema that cannot be read has an error: its secrets are unknown.
func SecretFields(schema []byte) ([]string, error) {
	var s struct {
		Properties map[string]struct {
			WriteOnly bool `json:"writeOnly"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		return nil, err
	}
	var out []string
	for name, p := range s.Properties {
		if p.WriteOnly {
			out = append(out, name)
		}
	}
	return out, nil
}

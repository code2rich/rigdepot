// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package localconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// FileName is the name of the configuration file inside the local data
// directory.
const FileName = "config.toml"

// Source says where a value came from. The JSON output of "config" uses the
// strings of String.
type Source int

const (
	SourceDefault Source = iota
	SourceFile
	SourceEnv
	SourceFlag
)

func (s Source) String() string {
	switch s {
	case SourceFile:
		return "file"
	case SourceEnv:
		return "env"
	case SourceFlag:
		return "flag"
	}
	return "default"
}

// Value is the effective value of one setting.
type Value struct {
	Key    string
	Str    string   // the value of every kind but KindList; "" when unset
	List   []string // the value of a KindList
	Source Source
	// Origin names where exactly the value came from: the environment
	// variable or the option. Empty for the file and the default.
	Origin string
	kind   Kind
}

// IsSet reports whether the setting has a value at all.
func (v Value) IsSet() bool {
	if v.kind == KindList {
		return len(v.List) > 0
	}
	return v.Str != ""
}

// Display formats the value on one line: a list joined by commas.
func (v Value) Display() string {
	if v.kind == KindList {
		return strings.Join(v.List, ",")
	}
	return v.Str
}

// IsList reports whether the setting holds a list.
func (v Value) IsList() bool { return v.kind == KindList }

// Warning is something that was ignored while loading. It is not an error:
// the command can go on, but the person should hear about it.
type Warning struct {
	Kind   WarningKind
	Key    string // the setting or the unknown key
	Origin string // the file path or the environment variable
}

// WarningKind classifies a Warning.
type WarningKind string

const (
	// WarnUnknownKey: the file has a key this version does not know. It is
	// kept as it is (a newer version may have written it) and not used.
	WarnUnknownKey WarningKind = "unknown_key"
	// WarnFlagOnly: a flag-only setting was found in the file or the
	// environment and ignored.
	WarnFlagOnly WarningKind = "flag_only"
	// WarnNotInFile: the file sets something that cannot be kept there,
	// such as home.
	WarnNotInFile WarningKind = "not_in_file"
)

// FileError means the configuration file exists but cannot be used: it
// cannot be read, it is not TOML, or a value in it is invalid. Its content is
// then unknown, never empty: nothing may overwrite it.
type FileError struct {
	Path string
	Err  error
}

func (e *FileError) Error() string { return e.Path + ": " + e.Err.Error() }
func (e *FileError) Unwrap() error { return e.Err }

// EnvError means an environment variable holds a value its setting does not
// accept.
type EnvError struct {
	Name string
	Err  *ValueError
}

func (e *EnvError) Error() string { return e.Name + ": " + e.Err.Error() }
func (e *EnvError) Unwrap() error { return e.Err }

// Config is the resolved local configuration.
type Config struct {
	path     string
	values   map[string]Value
	warnings []Warning
}

// Load resolves every setting from the environment and the file at path, on
// top of the defaults. Flags are applied afterwards with SetFlag. A missing
// file is no file. When the file or the environment cannot be used, Load
// still returns a Config without them (so that the caller can still pick a
// language and explain the problem) together with a *FileError or *EnvError.
func Load(path string, getenv func(string) string) (*Config, error) {
	c := &Config{path: path, values: map[string]Value{}}
	for _, s := range settings {
		v, _ := s.parse(s.Default)
		v.Source = SourceDefault
		c.values[s.Key] = v
	}
	var errs []error
	if err := c.loadFile(); err != nil {
		errs = append(errs, err)
	}
	if err := c.loadEnv(getenv); err != nil {
		errs = append(errs, err)
	}
	return c, errors.Join(errs...)
}

func (c *Config) loadFile() error {
	tree, err := readFile(c.path)
	if err != nil || tree == nil {
		return err
	}
	file := map[string]Value{}
	for _, entry := range flatten(tree, "") {
		// [server] url = ... where server is a value: not what this version
		// wrote, and not something to guess about.
		parts := strings.Split(entry.key, ".")
		for i := 1; i < len(parts); i++ {
			if _, ok := Lookup(strings.Join(parts[:i], ".")); ok {
				return &FileError{Path: c.path, Err: fmt.Errorf("%s is a table, but it is a single value", strings.Join(parts[:i], "."))}
			}
		}
		s, known := Lookup(entry.key)
		switch {
		case !known:
			c.warnings = append(c.warnings, Warning{Kind: WarnUnknownKey, Key: entry.key, Origin: c.path})
			continue
		case s.FlagOnly:
			c.warnings = append(c.warnings, Warning{Kind: WarnFlagOnly, Key: entry.key, Origin: c.path})
			continue
		case !s.InFile:
			c.warnings = append(c.warnings, Warning{Kind: WarnNotInFile, Key: entry.key, Origin: c.path})
			continue
		}
		raw, ok := fileValueString(entry.value, s.Kind)
		if !ok {
			return &FileError{Path: c.path, Err: fmt.Errorf("%s: wrong type %T", entry.key, entry.value)}
		}
		v, err := s.parse(raw)
		if err != nil {
			return &FileError{Path: c.path, Err: err}
		}
		v.Source = SourceFile
		file[s.Key] = v
	}
	// Only a file that was valid as a whole is applied.
	for k, v := range file {
		c.values[k] = v
	}
	return nil
}

func (c *Config) loadEnv(getenv func(string) string) error {
	var errs []error
	for _, s := range settings {
		for _, name := range s.Env {
			raw := getenv(name)
			if raw == "" {
				continue
			}
			v, err := s.parse(raw)
			if err != nil && name == "NO_COLOR" {
				v, err = s.parse("true")
			}
			if err != nil {
				var ve *ValueError
				errors.As(err, &ve)
				errs = append(errs, &EnvError{Name: name, Err: ve})
				break
			}
			v.Source, v.Origin = SourceEnv, name
			c.values[s.Key] = v
			break
		}
	}
	// A flag-only setting in the environment is ignored, and said so.
	for _, s := range settings {
		if s.FlagOnly {
			name := env(strings.ToUpper(s.Key))
			if getenv(name) != "" {
				c.warnings = append(c.warnings, Warning{Kind: WarnFlagOnly, Key: s.Key, Origin: name})
			}
		}
	}
	return errors.Join(errs...)
}

// SetFlag applies a value given on the command line; flag is the option as
// typed, for Value.Origin. It is the only way to set a flag-only setting.
func (c *Config) SetFlag(key, flag, raw string) error {
	s, ok := Lookup(key)
	if !ok {
		return fmt.Errorf("localconfig: unknown setting %q", key)
	}
	v, err := s.parse(raw)
	if err != nil {
		return err
	}
	v.Source, v.Origin = SourceFlag, flag
	c.values[key] = v
	return nil
}

// SetDefault replaces the default of a setting whose default depends on the
// machine, such as home (the person's home directory plus the local data
// directory name). A value from anywhere else stays.
func (c *Config) SetDefault(key, raw string) error {
	s, ok := Lookup(key)
	if !ok {
		return fmt.Errorf("localconfig: unknown setting %q", key)
	}
	if c.values[key].Source != SourceDefault {
		return nil
	}
	v, err := s.parse(raw)
	if err != nil {
		return err
	}
	c.values[key] = v
	return nil
}

// Get returns the effective value of a setting. An unknown key returns an
// unset value.
func (c *Config) Get(key string) Value { return c.values[key] }

// String returns the effective value of a scalar setting.
func (c *Config) String(key string) string { return c.values[key].Str }

// List returns the effective value of a list setting.
func (c *Config) List(key string) []string { return slices.Clone(c.values[key].List) }

// Bool returns the effective value of a boolean setting.
func (c *Config) Bool(key string) bool { return c.values[key].Str == "true" }

// Path returns the configuration file's path.
func (c *Config) Path() string { return c.path }

// Warnings returns what was ignored while loading.
func (c *Config) Warnings() []Warning { return slices.Clone(c.warnings) }

// readFile reads and parses the configuration file. A missing file gives nil
// and no error; anything else that goes wrong is a *FileError.
func readFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, &FileError{Path: path, Err: err}
	}
	tree := map[string]any{}
	if err := toml.Unmarshal(data, &tree); err != nil {
		return nil, &FileError{Path: path, Err: err}
	}
	return tree, nil
}

type flatEntry struct {
	key   string
	value any
}

// flatten turns nested tables into dotted keys: [repo] url = ... becomes
// repo.url.
func flatten(tree map[string]any, prefix string) []flatEntry {
	var out []flatEntry
	keys := make([]string, 0, len(tree))
	for k := range tree {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		full := prefix + k
		if sub, ok := tree[k].(map[string]any); ok {
			out = append(out, flatten(sub, full+".")...)
			continue
		}
		out = append(out, flatEntry{key: full, value: tree[k]})
	}
	return out
}

// fileValueString converts a TOML value to the text form parse takes.
func fileValueString(v any, kind Kind) (string, bool) {
	switch x := v.(type) {
	case string:
		if kind == KindList {
			return "", false
		}
		return x, true
	case bool:
		if kind != KindBool {
			return "", false
		}
		return fmt.Sprint(x), true
	case []any:
		if kind != KindList {
			return "", false
		}
		items := make([]string, 0, len(x))
		for _, item := range x {
			s, ok := item.(string)
			if !ok || strings.Contains(s, ",") {
				return "", false
			}
			items = append(items, s)
		}
		return strings.Join(items, ","), true
	}
	return "", false
}

// NotInFileError is returned by SaveFile for a setting the file cannot hold.
type NotInFileError struct{ Setting Setting }

func (e *NotInFileError) Error() string {
	return e.Setting.Key + " cannot be kept in the configuration file"
}

// UnknownKeyError is returned for a key that is not a setting.
type UnknownKeyError struct{ Key string }

func (e *UnknownKeyError) Error() string { return "unknown setting " + quote(e.Key) }

// SaveFile sets key to raw in the configuration file, or removes it when raw
// is empty, and returns the value as stored. The file is read again first:
// if it cannot be read or parsed, SaveFile returns a *FileError and does not
// write, so a file it does not understand is never replaced. Keys it does not
// know are kept. The previous file is kept next to it as config.toml.1,
// because rewriting it drops comments. The write is atomic: a crash leaves
// either the old or the new file.
func SaveFile(path, key, raw string) (Value, error) {
	s, ok := Lookup(key)
	if !ok {
		return Value{}, &UnknownKeyError{Key: key}
	}
	if !s.InFile {
		return Value{}, &NotInFileError{Setting: s}
	}
	var v Value
	if raw != "" {
		var err error
		if v, err = s.parse(raw); err != nil {
			return Value{}, err
		}
		v.Source = SourceFile
	}
	old, err := os.ReadFile(path)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Value{}, &FileError{Path: path, Err: err}
	}
	tree := map[string]any{}
	if exists {
		if err := toml.Unmarshal(old, &tree); err != nil {
			return Value{}, &FileError{Path: path, Err: err}
		}
		// A file Load would refuse is not rewritten either.
		probe := &Config{path: path, values: map[string]Value{}}
		if err := probe.loadFile(); err != nil {
			return Value{}, err
		}
	}
	if err := setPath(tree, strings.Split(key, "."), v, raw == ""); err != nil {
		return Value{}, &FileError{Path: path, Err: err}
	}
	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	if err := enc.Encode(tree); err != nil {
		return Value{}, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Value{}, err
	}
	if exists {
		if err := writeAtomic(path+".1", old); err != nil {
			return Value{}, err
		}
	}
	if err := writeAtomic(path, buf.Bytes()); err != nil {
		return Value{}, err
	}
	if raw == "" {
		d, _ := s.parse(s.Default)
		d.Source = SourceDefault
		return d, nil
	}
	return v, nil
}

// setPath sets or removes a dotted key in a decoded TOML tree, creating
// tables on the way. A non-table where a table is needed is an error: the
// file is not what this version expects.
func setPath(tree map[string]any, parts []string, v Value, remove bool) error {
	for _, p := range parts[:len(parts)-1] {
		next, ok := tree[p]
		if !ok {
			if remove {
				return nil
			}
			sub := map[string]any{}
			tree[p] = sub
			tree = sub
			continue
		}
		sub, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("%s is not a table", p)
		}
		tree = sub
	}
	last := parts[len(parts)-1]
	switch {
	case remove:
		delete(tree, last)
	case v.kind == KindList:
		items := make([]any, len(v.List))
		for i, s := range v.List {
			items[i] = s
		}
		tree[last] = items
	case v.kind == KindBool:
		tree[last] = v.Str == "true"
	default:
		tree[last] = v.Str
	}
	return nil
}

// writeAtomic writes data to a temporary file in the same directory, syncs
// it and renames it over path, with permissions for the owner only.
//
// TODO(M0-13): use the atomic write of internal/home once it exists.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // after a successful rename there is nothing left to remove
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

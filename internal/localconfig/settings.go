// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package localconfig

import (
	"net/url"
	"slices"
	"strings"

	"golang.org/x/text/language"

	"github.com/code2rich/rigdepot/internal/brand"
)

// Kind is the type of a setting's value.
type Kind int

const (
	KindString Kind = iota // any text
	KindList               // a list of names; on the command line and in RGD_* separated by commas
	KindBool               // true or false
	KindEnum               // one of Setting.Values
	KindURL                // an http or https address
	KindLang               // a BCP 47 language tag
	KindPath               // a file or directory
)

// Setting describes one local setting: where it may come from and what values
// it takes. The precedence is always flag > environment > file > default.
type Setting struct {
	Key     string   // the name in config.toml and in "config get", such as "repo.url"
	Flag    string   // the global option that sets it, such as "--lang"; empty when none
	Env     []string // environment variables that set it, first match wins
	InFile  bool     // whether config.toml may hold it
	Kind    Kind
	Values  []string // the allowed values of a KindEnum
	Default string   // the value when nothing sets it; "" means unset
	// FlagOnly settings are taken from the command line and nowhere else. A
	// value in an environment variable or in the configuration file is
	// ignored with a warning. --yes-global is one: "the person explicitly
	// agreed" means nothing if a file can agree for them.
	FlagOnly bool
}

// Keys of the settings other packages read.
const (
	KeyServer         = "server"
	KeyLang           = "lang"
	KeyHome           = "home"
	KeyRepoURL        = "repo.url"
	KeyRepoBranch     = "repo.branch"
	KeyRepoProducts   = "repo.products"
	KeyToolsDisabled  = "tools.disabled"
	KeyMCPMode        = "mcp.mode"
	KeyMCPCommand     = "mcp.command"
	KeyCAFile         = "net.ca_file"
	KeyGit            = "git"
	KeyNoColor        = "no_color"
	KeyNonInteractive = "non_interactive"
	KeyLogLevel       = "log_level"
	KeyYesGlobal      = "yes_global"
)

// Log levels, the values of KeyLogLevel. -v means debug and -q means error.
const (
	LogDebug = "debug"
	LogInfo  = "info"
	LogWarn  = "warn"
	LogError = "error"
)

func env(name string) string { return brand.EnvPrefix + name }

// settings lists every local setting in the order "config list" shows them.
// A new setting is one more entry here.
var settings = []Setting{
	{Key: KeyServer, Env: []string{env("SERVER")}, InFile: true, Kind: KindURL},
	{Key: KeyLang, Flag: "--lang", Env: []string{env("LANG")}, InFile: true, Kind: KindLang},
	{Key: KeyHome, Flag: "--home", Env: []string{env("HOME")}, Kind: KindPath},
	{Key: KeyRepoURL, Env: []string{env("REPO")}, InFile: true, Kind: KindString},
	{Key: KeyRepoBranch, InFile: true, Kind: KindString},
	{Key: KeyRepoProducts, InFile: true, Kind: KindList},
	{Key: KeyToolsDisabled, InFile: true, Kind: KindList},
	{Key: KeyMCPMode, InFile: true, Kind: KindEnum, Values: []string{"single-entry", "direct"}, Default: "single-entry"},
	{Key: KeyMCPCommand, InFile: true, Kind: KindEnum, Values: []string{"path", "name"}, Default: "path"},
	{Key: KeyCAFile, Env: []string{env("CA_FILE")}, InFile: true, Kind: KindPath},
	{Key: KeyGit, Env: []string{env("GIT")}, Kind: KindPath},
	// NO_COLOR is the cross-tool convention (no-color.org): any non-empty
	// value turns colour off.
	{Key: KeyNoColor, Flag: "--no-color", Env: []string{"NO_COLOR", env("NO_COLOR")}, Kind: KindBool, Default: "false"},
	{Key: KeyNonInteractive, Flag: "--non-interactive", Env: []string{env("NONINTERACTIVE")}, Kind: KindBool, Default: "false"},
	{Key: KeyLogLevel, Flag: "-v / -q", Env: []string{env("LOG_LEVEL")}, Kind: KindEnum, Values: []string{LogDebug, LogInfo, LogWarn, LogError}, Default: LogInfo},
	{Key: KeyYesGlobal, Flag: "--yes-global", Kind: KindBool, Default: "false", FlagOnly: true},
}

// Settings returns every local setting in display order.
func Settings() []Setting { return slices.Clone(settings) }

// Lookup returns the setting with the given key.
func Lookup(key string) (Setting, bool) {
	for _, s := range settings {
		if s.Key == key {
			return s, true
		}
	}
	return Setting{}, false
}

// Reason says why a value was rejected, so that the caller can explain it in
// the person's language.
type Reason string

const (
	ReasonEnum Reason = "enum" // not one of Setting.Values
	ReasonURL  Reason = "url"  // not an http or https address
	ReasonLang Reason = "lang" // not a language tag
	ReasonBool Reason = "bool" // not true or false
)

// ValueError is a value a setting does not accept.
type ValueError struct {
	Key    string
	Value  string
	Reason Reason
	// Allowed lists the accepted values for ReasonEnum and ReasonBool.
	Allowed []string
}

func (e *ValueError) Error() string {
	msg := "invalid value " + quote(e.Value) + " for " + e.Key + " (" + string(e.Reason)
	if len(e.Allowed) > 0 {
		msg += ": " + strings.Join(e.Allowed, ", ")
	}
	return msg + ")"
}

func quote(s string) string { return `"` + s + `"` }

// parse checks raw against the setting and returns it in canonical form: a
// language tag in its canonical spelling, a boolean as "true" or "false", a
// list as its trimmed, non-empty items.
func (s Setting) parse(raw string) (Value, error) {
	v := Value{Key: s.Key, kind: s.Kind}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return v, nil // unset
	}
	switch s.Kind {
	case KindList:
		for _, item := range strings.Split(raw, ",") {
			if item = strings.TrimSpace(item); item != "" {
				v.List = append(v.List, item)
			}
		}
		return v, nil
	case KindBool:
		switch strings.ToLower(raw) {
		case "1", "true", "yes", "on":
			v.Str = "true"
		case "0", "false", "no", "off":
			v.Str = "false"
		default:
			return v, &ValueError{Key: s.Key, Value: raw, Reason: ReasonBool, Allowed: []string{"true", "false"}}
		}
		return v, nil
	case KindEnum:
		if !slices.Contains(s.Values, raw) {
			return v, &ValueError{Key: s.Key, Value: raw, Reason: ReasonEnum, Allowed: s.Values}
		}
	case KindURL:
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return v, &ValueError{Key: s.Key, Value: raw, Reason: ReasonURL}
		}
	case KindLang:
		tag, err := language.Parse(strings.ReplaceAll(raw, "_", "-"))
		if err != nil || tag == language.Und {
			return v, &ValueError{Key: s.Key, Value: raw, Reason: ReasonLang}
		}
		raw = tag.String()
	}
	v.Str = raw
	return v, nil
}

// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package config

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/code2rich/rigdepot/internal/brand"
)

// Setting describes one startup setting: where it can be given and whether
// its value is a secret. The list is the configuration table of
// docs/architecture/08-deployment.md#配置.
type Setting struct {
	Key      string // key in the configuration file, also the name used in messages
	Env      string // environment variable; "" when there is none
	Flag     string // command-line flag without dashes; "" when there is none
	FileForm bool   // the value can also come from the file named by <Env>_FILE
	InFile   bool   // the setting may appear in the configuration file
	Secret   bool   // never printed; not available as a flag (it would show in ps)
	Help     string
}

// settings is the configuration table. Order matters only for display.
var settings = []Setting{
	{Key: "base_url", Env: EnvName("BASE_URL"), Flag: "base-url", InFile: true, Help: "public address, such as https://hub.example.com"},
	{Key: "listen", Env: EnvName("LISTEN"), Flag: "listen", InFile: true, Help: "address to listen on (default " + defaultListen + ")"},
	{Key: "data_dir", Env: EnvName("DATA_DIR"), Flag: "data-dir", InFile: true, Help: "data directory (default " + defaultDataDir + ")"},
	{Key: "database_url", Env: EnvName("DATABASE_URL"), FileForm: true, InFile: true, Secret: true, Help: "sqlite://<path> or postgres://…"},
	{Key: "master_key_file", Env: EnvName("MASTER_KEY_FILE"), Flag: "master-key-file", InFile: true, Help: "master key file (default <data dir>/" + defaultKeyName + ")"},
	{Key: "master_key", Env: EnvName("MASTER_KEY"), Secret: true, Help: "master key, 32 bytes in base64"},
	{Key: "setup_token", Env: EnvName("SETUP_TOKEN"), FileForm: true, Secret: true, Help: "one-time setup token"},
	{Key: "auto_migrate", Env: EnvName("AUTO_MIGRATE"), Flag: "auto-migrate", InFile: true, Help: "run database migrations on start (default true)"},
	{Key: "tls.cert_file", Env: EnvName("TLS_CERT_FILE"), Flag: "tls-cert-file", InFile: true, Help: "TLS certificate file"},
	{Key: "tls.key_file", Env: EnvName("TLS_KEY_FILE"), Flag: "tls-key-file", InFile: true, Help: "TLS private key file"},
	{Key: "tls.acme_domains", Env: EnvName("TLS_ACME_DOMAINS"), Flag: "tls-acme-domains", InFile: true, Help: "domains to get certificates for automatically, comma-separated"},
	{Key: "tls.acme_email", Env: EnvName("TLS_ACME_EMAIL"), Flag: "tls-acme-email", InFile: true, Help: "contact address for the certificate authority"},
	{Key: "tls.http_redirect_listen", Env: EnvName("HTTP_REDIRECT_LISTEN"), Flag: "http-redirect-listen", InFile: true, Help: "plain HTTP address that only redirects to HTTPS, such as :80"},
	{Key: "trusted_proxies", Env: EnvName("TRUSTED_PROXIES"), Flag: "trusted-proxies", InFile: true, Help: "address ranges of the reverse proxy, comma-separated"},
	{Key: "insecure_http", Env: EnvName("INSECURE_HTTP"), Flag: "insecure-http", Help: "allow plain HTTP on a network address (isolated trials only)"},
	{Key: "default_locale", Env: EnvName("DEFAULT_LOCALE"), Flag: "default-locale", InFile: true, Help: "language when neither the user nor the browser says (default " + defaultLocale + ")"},
	{Key: "log.level", Env: EnvName("LOG_LEVEL"), Flag: "log-level", InFile: true, Help: "debug, info, warn or error (default info)"},
	{Key: "log.format", Env: EnvName("LOG_FORMAT"), Flag: "log-format", InFile: true, Help: "json or text (default json)"},
	{Key: "metrics_addr", Env: EnvName("METRICS_ADDR"), Flag: "metrics-addr", InFile: true, Help: "address for metrics (default off)"},
	{Key: "git", Env: EnvName("GIT"), Flag: "git", InFile: true, Help: "git executable (default git)"},
	{Key: "source_sync_interval", Env: EnvName("SOURCE_SYNC_INTERVAL"), Flag: "source-sync-interval", InFile: true, Help: "how often to fetch resource repositories (default 10m)"},
	{Key: "max_bundle_mb", Env: EnvName("MAX_BUNDLE_MB"), Flag: "max-bundle-mb", InFile: true, Help: "largest resource bundle in MB (default 5)"},
	{Key: "cli_dist_dir", Env: EnvName("CLI_DIST_DIR"), Flag: "cli-dist-dir", InFile: true, Help: "directory with the release archives of the command-line program to hand out (default: <data_dir>/cli, then /usr/share/" + brand.CLILong + "/cli)"},
	{Key: "cli_download_base", Env: EnvName("CLI_DOWNLOAD_BASE"), Flag: "cli-download-base", InFile: true, Help: "URL of a directory with a release's checksums.txt and archives, for the install scripts when no archives are on this machine"},
	{Key: "backup.dir", Env: EnvName("BACKUP_DIR"), Flag: "backup-dir", InFile: true, Help: "directory for daily backups (default off)"},
	{Key: "backup.keep", Env: EnvName("BACKUP_KEEP"), Flag: "backup-keep", InFile: true, Help: "how many backups to keep (default 14)"},
	{Key: "web_dev_proxy", Env: EnvName("WEB_DEV_PROXY"), Flag: "web-dev-proxy", Help: "development only: serve the console from this local development server (such as http://127.0.0.1:5173) instead of the embedded files"},
	{Key: "modules_disabled", Env: EnvName("MODULES_DISABLED"), Flag: "modules-disabled", InFile: true, Help: "modules never to load, comma-separated"},
}

// Settings returns the configuration table.
func Settings() []Setting { return slices.Clone(settings) }

// ConfigEnv is the variable that names the configuration file.
var ConfigEnv = EnvName("CONFIG")

// Options are the inputs of Load. The zero value reads the real environment
// and file system.
type Options struct {
	// LookupEnv reads an environment variable. Defaults to os.LookupEnv.
	LookupEnv func(string) (string, bool)
	// ReadFile reads the configuration file and *_FILE values. Defaults to
	// os.ReadFile.
	ReadFile func(string) ([]byte, error)
	// ConfigFile is the --config flag; it wins over the variable.
	ConfigFile string
	// Flags are the values given on the command line, by setting key.
	Flags map[string]string
}

type rawValue struct {
	value  string
	origin Origin
}

// Load reads the configuration. Precedence, highest first: command-line
// flags, environment variables, the configuration file, defaults. An empty
// environment variable counts as not set. Every problem is reported, not
// just the first.
func Load(opts Options) (*Config, error) {
	lookup := opts.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	env := func(name string) (string, bool) {
		v, ok := lookup(name)
		if !ok || strings.TrimSpace(v) == "" {
			return "", false
		}
		return v, true
	}
	readFile := opts.ReadFile
	if readFile == nil {
		readFile = os.ReadFile
	}

	raw := map[string]rawValue{}
	var errs []error

	file := opts.ConfigFile
	if file == "" {
		file, _ = env(ConfigEnv)
	}
	if file != "" {
		values, err := readConfigFile(file, readFile)
		if err != nil {
			return nil, err
		}
		for k, v := range values {
			raw[k] = rawValue{v, Origin{FromFile, file}}
		}
	}

	for _, s := range settings {
		if s.Env == "" {
			continue
		}
		v, direct := env(s.Env)
		if s.FileForm {
			path, viaFile := env(s.Env + "_FILE")
			if direct && viaFile {
				errs = append(errs, fmt.Errorf("%s and %s_FILE are both set; use one", s.Env, s.Env))
				continue
			}
			if viaFile {
				data, err := readFile(path)
				if err != nil {
					errs = append(errs, fmt.Errorf("%s_FILE: %w", s.Env, err))
					continue
				}
				raw[s.Key] = rawValue{strings.TrimSpace(string(data)), Origin{FromEnv, s.Env + "_FILE"}}
				continue
			}
		}
		if direct {
			if !s.Secret {
				v = strings.TrimSpace(v)
			}
			raw[s.Key] = rawValue{v, Origin{FromEnv, s.Env}}
		}
	}

	for key, v := range opts.Flags {
		s, ok := settingByKey(key)
		if !ok || s.Flag == "" {
			errs = append(errs, fmt.Errorf("%q cannot be given as a flag", key))
			continue
		}
		raw[key] = rawValue{strings.TrimSpace(v), Origin{FromFlag, "--" + s.Flag}}
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	c := &Config{File: file, Origins: map[string]Origin{}}
	for _, s := range settings {
		if v, ok := raw[s.Key]; ok {
			c.Origins[s.Key] = v.origin
		} else {
			c.Origins[s.Key] = Origin{Kind: FromDefault}
		}
	}
	if err := c.apply(raw); err != nil {
		return nil, err
	}
	return c, nil
}

func settingByKey(key string) (Setting, bool) {
	for _, s := range settings {
		if s.Key == key {
			return s, true
		}
	}
	return Setting{}, false
}

// readConfigFile reads the TOML configuration file and flattens it into
// setting keys: [tls] cert_file = "…" becomes "tls.cert_file". Lists become
// comma-separated strings. An unknown key is an error, so that a typo does
// not silently leave a default in place.
func readConfigFile(path string, readFile func(string) ([]byte, error)) (map[string]string, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, fmt.Errorf("configuration file: %w", err)
	}
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("configuration file %s: %w", path, err)
	}
	out := map[string]string{}
	var errs []error
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for k, v := range m {
			key := prefix + k
			if sub, ok := v.(map[string]any); ok {
				walk(key+".", sub)
				continue
			}
			s, ok := settingByKey(key)
			if !ok || !s.InFile {
				hint := ""
				if ok && s.Env != "" {
					hint = fmt.Sprintf(" (it can only be given as %s)", s.Env)
				}
				errs = append(errs, fmt.Errorf("configuration file %s: unknown key %q%s", path, key, hint))
				continue
			}
			str, err := scalarString(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("configuration file %s: %s: %v", path, key, err))
				continue
			}
			out[key] = str
		}
	}
	walk("", doc)
	if len(errs) > 0 {
		sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
		return nil, errors.Join(errs...)
	}
	return out, nil
}

func scalarString(v any) (string, error) {
	switch v := v.(type) {
	case string:
		return v, nil
	case bool:
		return strconv.FormatBool(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return "", fmt.Errorf("want a list of strings")
			}
			parts = append(parts, s)
		}
		return strings.Join(parts, ","), nil
	}
	return "", fmt.Errorf("want a string, a whole number, true or false, or a list of strings")
}

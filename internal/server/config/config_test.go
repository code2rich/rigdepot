// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// base is the smallest valid configuration.
var base = map[string]string{EnvName("BASE_URL"): "https://hub.example.com"}

func envOf(vars ...map[string]string) func(string) (string, bool) {
	all := map[string]string{}
	for _, m := range vars {
		for k, v := range m {
			all[k] = v
		}
	}
	return func(k string) (string, bool) { v, ok := all[k]; return v, ok }
}

func files(m map[string]string) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if v, ok := m[p]; ok {
			return []byte(v), nil
		}
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
}

const masterKeyB64 = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=" // bytes 0..31, not a real key

// sample is, for one setting, a value to give it and how to read it back
// from the loaded configuration.
type sample struct {
	env  string // value of the environment variable
	toml string // the same value in TOML; "" when the setting is not in the file
	got  func(*Config) string
	want string
}

func samples() map[string]sample {
	return map[string]sample{
		"base_url":        {"https://hub.example.com/rgd/", `"https://hub.example.com/rgd/"`, func(c *Config) string { return c.BaseURL.String() }, "https://hub.example.com/rgd"},
		"listen":          {"127.0.0.1:9000", `"127.0.0.1:9000"`, func(c *Config) string { return c.Listen }, "127.0.0.1:9000"},
		"data_dir":        {"/srv/data", `"/srv/data"`, func(c *Config) string { return filepath.ToSlash(c.DataDir) }, "/srv/data"},
		"database_url":    {"postgres://app@db.example.com/x", `"postgres://app@db.example.com/x"`, func(c *Config) string { return c.DatabaseURL }, "postgres://app@db.example.com/x"},
		"master_key_file": {"/srv/key", `"/srv/key"`, func(c *Config) string { return c.MasterKeyFile }, "/srv/key"},
		"master_key":      {masterKeyB64, "", func(c *Config) string { return fmt.Sprint(len(c.MasterKey), c.MasterKey[31]) }, "32 31"},
		"setup_token":     {"<placeholder-setup-token>", "", func(c *Config) string { return c.SetupToken }, "<placeholder-setup-token>"},
		"auto_migrate":    {"false", `false`, func(c *Config) string { return fmt.Sprint(c.AutoMigrate) }, "false"},
		"tls.cert_file":   {"/srv/cert.pem", `"/srv/cert.pem"`, func(c *Config) string { return c.TLS.CertFile }, "/srv/cert.pem"},
		"tls.key_file":    {"/srv/key.pem", `"/srv/key.pem"`, func(c *Config) string { return c.TLS.KeyFile }, "/srv/key.pem"},
		"tls.acme_domains": {"hub.example.com, www.example.com", `["hub.example.com", "www.example.com"]`,
			func(c *Config) string { return strings.Join(c.TLS.ACMEDomains, "|") }, "hub.example.com|www.example.com"},
		"tls.acme_email":           {"ops@example.com", `"ops@example.com"`, func(c *Config) string { return c.TLS.ACMEEmail }, "ops@example.com"},
		"tls.http_redirect_listen": {":80", `":80"`, func(c *Config) string { return c.TLS.HTTPRedirectListen }, ":80"},
		"trusted_proxies":          {"10.0.0.0/8,192.0.2.7", `["10.0.0.0/8", "192.0.2.7"]`, func(c *Config) string { return fmt.Sprint(c.TrustedProxies) }, "[10.0.0.0/8 192.0.2.7/32]"},
		"insecure_http":            {"1", "", func(c *Config) string { return fmt.Sprint(c.InsecureHTTP) }, "true"},
		"default_locale":           {"zh-CN", `"zh-CN"`, func(c *Config) string { return c.DefaultLocale }, "zh-CN"},
		"log.level":                {"debug", `"debug"`, func(c *Config) string { return c.LogLevel }, "debug"},
		"log.format":               {"text", `"text"`, func(c *Config) string { return c.LogFormat }, "text"},
		"metrics_addr":             {"127.0.0.1:9464", `"127.0.0.1:9464"`, func(c *Config) string { return c.MetricsAddr }, "127.0.0.1:9464"},
		"git":                      {"/opt/git/bin/git", `"/opt/git/bin/git"`, func(c *Config) string { return c.Git }, "/opt/git/bin/git"},
		"source_sync_interval":     {"30m", `"30m"`, func(c *Config) string { return c.SourceSyncInterval.String() }, (30 * time.Minute).String()},
		"max_bundle_mb":            {"8", `8`, func(c *Config) string { return fmt.Sprint(c.MaxBundleMB) }, "8"},
		"cli_dist_dir":             {"/srv/cli", `"/srv/cli"`, func(c *Config) string { return c.CLIDistDir }, "/srv/cli"},
		"cli_download_base":        {"https://downloads.example.com/rgd/", `"https://downloads.example.com/rgd/"`, func(c *Config) string { return c.CLIDownloadBase }, "https://downloads.example.com/rgd"},
		"backup.dir":               {"/srv/backups", `"/srv/backups"`, func(c *Config) string { return c.BackupDir }, "/srv/backups"},
		"backup.keep":              {"3", `3`, func(c *Config) string { return fmt.Sprint(c.BackupKeep) }, "3"},
		"web_dev_proxy":            {"http://127.0.0.1:5173/", "", func(c *Config) string { return c.WebDevProxy.String() }, "http://127.0.0.1:5173"},
		"modules_disabled":         {"insights, ai-review", `["insights", "ai-review"]`, func(c *Config) string { return strings.Join(c.ModulesDisabled, "|") }, "insights|ai-review"},
	}
}

// companions are other settings a sample needs to be valid on its own.
func companions(key string) map[string]string {
	switch key {
	case "tls.cert_file", "tls.key_file", "tls.http_redirect_listen":
		return map[string]string{"tls.cert_file": "/srv/cert.pem", "tls.key_file": "/srv/key.pem"}
	case "tls.acme_email":
		return map[string]string{"tls.acme_domains": "hub.example.com"}
	}
	return nil
}

func settingFor(t *testing.T, key string) Setting {
	t.Helper()
	s, ok := settingByKey(key)
	if !ok {
		t.Fatalf("no setting %q", key)
	}
	return s
}

// TestEverySettingFromEnvironment is the acceptance check "every item of the
// configuration table can be read from the environment".
func TestEverySettingFromEnvironment(t *testing.T) {
	all := samples()
	for _, s := range Settings() {
		smp, ok := all[s.Key]
		if !ok {
			t.Errorf("setting %s has no sample in this test", s.Key)
			continue
		}
		t.Run(s.Key, func(t *testing.T) {
			vars := map[string]string{s.Env: smp.env}
			for k, v := range companions(s.Key) {
				if k != s.Key {
					vars[settingFor(t, k).Env] = v
				}
			}
			c, err := Load(Options{LookupEnv: envOf(base, vars)})
			if err != nil {
				t.Fatal(err)
			}
			if got := smp.got(c); got != smp.want {
				t.Errorf("got %q, want %q", got, smp.want)
			}
			if o := c.Origins[s.Key]; o.Kind != FromEnv || o.Detail != s.Env {
				t.Errorf("origin = %v, want env %s", o, s.Env)
			}
		})
	}
}

// TestEverySettingFromFile is the same check for the configuration file:
// every setting with a file key can be read from it.
func TestEverySettingFromFile(t *testing.T) {
	all := samples()
	for _, s := range Settings() {
		smp := all[s.Key]
		if !s.InFile {
			if smp.toml != "" {
				t.Errorf("%s is not allowed in the file, but the sample has a TOML value", s.Key)
			}
			continue
		}
		t.Run(s.Key, func(t *testing.T) {
			lines := map[string]string{s.Key: smp.toml}
			for k, v := range companions(s.Key) {
				if k != s.Key {
					lines[k] = fmt.Sprintf("%q", v)
				}
			}
			if s.Key != "base_url" {
				lines["base_url"] = `"https://hub.example.com"`
			}
			c, err := Load(Options{
				LookupEnv: envOf(map[string]string{ConfigEnv: "/etc/server.toml"}),
				ReadFile:  files(map[string]string{"/etc/server.toml": toTOML(lines)}),
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := smp.got(c); got != smp.want {
				t.Errorf("got %q, want %q", got, smp.want)
			}
			if o := c.Origins[s.Key]; o.Kind != FromFile {
				t.Errorf("origin = %v, want file", o)
			}
		})
	}
}

// toTOML writes dotted keys as tables, the way an administrator would.
func toTOML(lines map[string]string) string {
	var top, tables strings.Builder
	sections := map[string][]string{}
	for k, v := range lines {
		if table, key, ok := strings.Cut(k, "."); ok {
			sections[table] = append(sections[table], key+" = "+v)
		} else {
			top.WriteString(k + " = " + v + "\n")
		}
	}
	for table, kv := range sections {
		tables.WriteString("\n[" + table + "]\n" + strings.Join(kv, "\n") + "\n")
	}
	return top.String() + tables.String()
}

func TestDefaults(t *testing.T) {
	c, err := Load(Options{LookupEnv: envOf(base)})
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string][2]string{
		"listen":       {c.Listen, "127.0.0.1:8080"},
		"database_url": {c.DatabaseURL, SchemeSQLite + "://" + filepath.ToSlash(filepath.Join(c.DataDir, defaultDBName))},
		"master_key":   {c.MasterKeyFile, filepath.Join(c.DataDir, "master.key")},
		"auto_migrate": {fmt.Sprint(c.AutoMigrate), "true"},
		"locale":       {c.DefaultLocale, "en"},
		"log":          {c.LogLevel + "/" + c.LogFormat, "info/json"},
		"git":          {c.Git, "git"},
		"interval":     {c.SourceSyncInterval.String(), "10m0s"},
		"bundle":       {fmt.Sprint(c.MaxBundleMB), "5"},
		"keep":         {fmt.Sprint(c.BackupKeep), "14"},
		"metrics":      {c.MetricsAddr, ""},
		"backup":       {c.BackupDir, ""},
	}
	for name, v := range checks {
		if v[0] != v[1] {
			t.Errorf("%s = %q, want %q", name, v[0], v[1])
		}
	}
	if c.Origins["listen"].Kind != FromDefault || c.DatabaseScheme() != SchemeSQLite {
		t.Errorf("origin %v, scheme %s", c.Origins["listen"], c.DatabaseScheme())
	}
}

func TestDefaultsFollowTheDataDirectory(t *testing.T) {
	c, err := Load(Options{LookupEnv: envOf(base, map[string]string{EnvName("DATA_DIR"): "/srv/rd"})})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(c.DatabaseURL, "/srv/rd/"+defaultDBName) || c.MasterKeyFile != filepath.Join("/srv/rd", "master.key") {
		t.Errorf("database %s, master key %s", c.DatabaseURL, c.MasterKeyFile)
	}
	// The command line is looked for in the data directory first, then where the packages
	// and the image put it.
	if want := []string{filepath.Join("/srv/rd", "cli"), defaultCLIDist}; !slices.Equal(c.CLIDistDirs, want) || c.CLIDistDir != defaultCLIDist {
		t.Errorf("command-line directories %q (%s), want %q", c.CLIDistDirs, c.CLIDistDir, want)
	}
	// A configured directory is the only one.
	c, err = Load(Options{LookupEnv: envOf(base, map[string]string{EnvName("CLI_DIST_DIR"): "/srv/cli"})})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.CLIDistDirs, []string{"/srv/cli"}) {
		t.Errorf("configured: %q", c.CLIDistDirs)
	}
}

func TestPrecedence(t *testing.T) {
	file := files(map[string]string{"/etc/a.toml": "base_url = \"https://file.example.com\"\nlisten = \"127.0.0.1:1\"\nlog.level = \"warn\"\n"})
	c, err := Load(Options{
		LookupEnv:  envOf(map[string]string{ConfigEnv: "/etc/ignored.toml", EnvName("LISTEN"): "127.0.0.1:2", EnvName("LOG_LEVEL"): "error"}),
		ReadFile:   file,
		ConfigFile: "/etc/a.toml", // the --config flag wins over the variable
		Flags:      map[string]string{"log.level": "debug"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL.Host != "file.example.com" || c.Listen != "127.0.0.1:2" || c.LogLevel != "debug" {
		t.Errorf("base %s listen %s level %s; want file, env, flag", c.BaseURL, c.Listen, c.LogLevel)
	}
	if c.Origins["log.level"].Kind != FromFlag || c.Origins["listen"].Kind != FromEnv || c.Origins["base_url"].Kind != FromFile {
		t.Errorf("origins %v", c.Origins)
	}
}

func TestEmptyVariableCountsAsUnset(t *testing.T) {
	c, err := Load(Options{LookupEnv: envOf(base, map[string]string{EnvName("LISTEN"): "", EnvName("DATABASE_URL_FILE"): ""})})
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != defaultListen || c.DatabaseScheme() != SchemeSQLite {
		t.Errorf("listen %s, database %s", c.Listen, c.DatabaseURL)
	}
}

func TestFileFormOfSecrets(t *testing.T) {
	read := files(map[string]string{"/run/secrets/db": "postgres://app:placeholder@db.example.com/x\n", "/run/secrets/setup": "<placeholder-token>\n"})
	c, err := Load(Options{
		LookupEnv: envOf(base, map[string]string{EnvName("DATABASE_URL_FILE"): "/run/secrets/db", EnvName("SETUP_TOKEN_FILE"): "/run/secrets/setup"}),
		ReadFile:  read,
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.DatabaseURL != "postgres://app:placeholder@db.example.com/x" || c.SetupToken != "<placeholder-token>" {
		t.Errorf("database %q, setup token %q; want the trimmed file contents", c.DatabaseURL, c.SetupToken)
	}
	if c.Origins["database_url"].Detail != EnvName("DATABASE_URL_FILE") {
		t.Errorf("origin %v", c.Origins["database_url"])
	}

	_, err = Load(Options{LookupEnv: envOf(base, map[string]string{EnvName("DATABASE_URL"): "sqlite:///x.db", EnvName("DATABASE_URL_FILE"): "/run/secrets/db"}), ReadFile: read})
	if err == nil || !strings.Contains(err.Error(), "both set") {
		t.Errorf("both forms: err = %v", err)
	}
	_, err = Load(Options{LookupEnv: envOf(base, map[string]string{EnvName("SETUP_TOKEN_FILE"): "/run/secrets/missing"}), ReadFile: read})
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file: err = %v", err)
	}
}

func TestRejections(t *testing.T) {
	tests := map[string]struct {
		env  map[string]string
		file string
		want string
	}{
		"no base url":            {env: map[string]string{}, want: "base_url is required"},
		"relative base url":      {env: map[string]string{EnvName("BASE_URL"): "hub.example.com"}, want: "absolute"},
		"base url with password": {env: map[string]string{EnvName("BASE_URL"): "https://a:b@hub.example.com"}, want: "only a scheme"},
		"bad listen":             {env: map[string]string{EnvName("LISTEN"): "8080"}, want: "listen"},
		"bad database":           {env: map[string]string{EnvName("DATABASE_URL"): "mysql://x"}, want: "database_url"},
		"short master key":       {env: map[string]string{EnvName("MASTER_KEY"): "c2hvcnQ="}, want: "32 bytes"},
		"key and key file": {env: map[string]string{EnvName("MASTER_KEY"): masterKeyB64, EnvName("MASTER_KEY_FILE"): "/k"},
			want: "not both"},
		"cert without key":     {env: map[string]string{EnvName("TLS_CERT_FILE"): "/c"}, want: "go together"},
		"cert and acme":        {env: map[string]string{EnvName("TLS_CERT_FILE"): "/c", EnvName("TLS_KEY_FILE"): "/k", EnvName("TLS_ACME_DOMAINS"): "hub.example.com"}, want: "exclude"},
		"redirect without tls": {env: map[string]string{EnvName("HTTP_REDIRECT_LISTEN"): ":80"}, want: "needs tls"},
		"bad proxy":            {env: map[string]string{EnvName("TRUSTED_PROXIES"): "10.0.0.0/8,proxy.example.com"}, want: "proxy.example.com"},
		"bad bool":             {env: map[string]string{EnvName("AUTO_MIGRATE"): "maybe"}, want: "auto_migrate"},
		"bad level":            {env: map[string]string{EnvName("LOG_LEVEL"): "loud"}, want: "log.level"},
		"metrics on listen":    {env: map[string]string{EnvName("METRICS_ADDR"): "127.0.0.1:8080"}, want: "public port"},
		"short interval":       {env: map[string]string{EnvName("SOURCE_SYNC_INTERVAL"): "5s"}, want: "at least 1m"},
		"bad module":           {env: map[string]string{EnvName("MODULES_DISABLED"): "Insights"}, want: "module identifier"},
		"remote dev proxy":     {env: map[string]string{EnvName("WEB_DEV_PROXY"): "http://dev.example.com:5173"}, want: "on this machine"},
		"dev proxy with path":  {env: map[string]string{EnvName("WEB_DEV_PROXY"): "http://127.0.0.1:5173/app"}, want: "web_dev_proxy"},
		"http download base":   {env: map[string]string{EnvName("CLI_DOWNLOAD_BASE"): "http://downloads.example.com"}, want: "https://"},
		"unknown file key":     {file: "base_url = \"https://hub.example.com\"\nlisten_addr = \"x\"\n", want: `unknown key "listen_addr"`},
		"secret in file":       {file: "base_url = \"https://hub.example.com\"\nmaster_key = \"x\"\n", want: "only be given as " + EnvName("MASTER_KEY")},
		"broken file":          {file: "base_url = \n", want: "configuration file"},
		"float in file":        {file: "base_url = \"https://hub.example.com\"\nmax_bundle_mb = 1.5\n", want: "max_bundle_mb"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			env := map[string]string{}
			if _, ok := tt.env[EnvName("BASE_URL")]; !ok && tt.file == "" && name != "no base url" {
				env[EnvName("BASE_URL")] = "https://hub.example.com"
			}
			for k, v := range tt.env {
				env[k] = v
			}
			opts := Options{LookupEnv: envOf(env)}
			if tt.file != "" {
				opts.ConfigFile = "/etc/c.toml"
				opts.ReadFile = files(map[string]string{"/etc/c.toml": tt.file})
			}
			_, err := Load(opts)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want one mentioning %q", err, tt.want)
			}
		})
	}
}

func TestErrorsDoNotRepeatTheDatabasePassword(t *testing.T) {
	_, err := Load(Options{LookupEnv: envOf(base, map[string]string{EnvName("DATABASE_URL"): "postgres://app:sEcReT%zz@db.example.com/x"})})
	if err == nil || strings.Contains(err.Error(), "sEcReT") {
		t.Errorf("err = %v", err)
	}
}

func TestFlagsOnlyForNonSecretSettings(t *testing.T) {
	for _, s := range Settings() {
		if s.Secret && s.Flag != "" {
			t.Errorf("%s is a secret and has a flag; flags show up in the process list", s.Key)
		}
	}
	_, err := Load(Options{LookupEnv: envOf(base), Flags: map[string]string{"master_key": masterKeyB64}})
	if err == nil {
		t.Error("a secret given as a flag was accepted")
	}
}

// TestPlainHTTPBeyondThisMachine is the acceptance check "plain HTTP on a
// non-local address is refused".
func TestPlainHTTPBeyondThisMachine(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		ok   bool
	}{
		{"default: https base, loopback listen", map[string]string{}, true},
		{"local trial", map[string]string{EnvName("BASE_URL"): "http://127.0.0.1:8080"}, true},
		{"localhost trial", map[string]string{EnvName("BASE_URL"): "http://localhost:8080", EnvName("LISTEN"): "localhost:8080"}, true},
		{"ipv6 loopback", map[string]string{EnvName("BASE_URL"): "http://[::1]:8080", EnvName("LISTEN"): "[::1]:8080"}, true},
		{"http base on a network name", map[string]string{EnvName("BASE_URL"): "http://hub.example.com"}, false},
		{"http base even behind a proxy", map[string]string{EnvName("BASE_URL"): "http://hub.example.com", EnvName("TRUSTED_PROXIES"): "10.0.0.0/8"}, false},
		{"all interfaces, no proxy", map[string]string{EnvName("LISTEN"): "0.0.0.0:8080"}, false},
		{"empty host is all interfaces", map[string]string{EnvName("LISTEN"): ":8080"}, false},
		{"a network address", map[string]string{EnvName("LISTEN"): "192.0.2.10:8080"}, false},
		{"loopback base, network listen", map[string]string{EnvName("BASE_URL"): "http://127.0.0.1:8080", EnvName("LISTEN"): "0.0.0.0:8080"}, false},
		{"declared reverse proxy", map[string]string{EnvName("LISTEN"): "0.0.0.0:8080", EnvName("TRUSTED_PROXIES"): "172.16.0.0/12"}, true},
		{"own certificate", map[string]string{EnvName("LISTEN"): "0.0.0.0:8443", EnvName("TLS_CERT_FILE"): "/c", EnvName("TLS_KEY_FILE"): "/k"}, true},
		{"acme", map[string]string{EnvName("LISTEN"): ":443", EnvName("TLS_ACME_DOMAINS"): "hub.example.com"}, true},
		{"explicitly insecure", map[string]string{EnvName("BASE_URL"): "http://hub.example.com", EnvName("LISTEN"): "0.0.0.0:8080", EnvName("INSECURE_HTTP"): "1"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(Options{LookupEnv: envOf(base, tt.env)})
			if tt.ok && err != nil {
				t.Errorf("refused: %v", err)
			}
			if !tt.ok && !errors.Is(err, ErrPlaintext) {
				t.Errorf("err = %v, want ErrPlaintext", err)
			}
		})
	}
}

func TestReadsTheRealFileSystemByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.toml")
	if err := os.WriteFile(path, []byte("base_url = \"https://hub.example.com\"\n[log]\nformat = \"text\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(Options{ConfigFile: path, LookupEnv: envOf()})
	if err != nil {
		t.Fatal(err)
	}
	if c.LogFormat != "text" || c.File != path {
		t.Errorf("format %s, file %s", c.LogFormat, c.File)
	}
}

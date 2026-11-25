// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/code2rich/rigdepot/internal/server/config"
)

// runConfigCheck is "config check" (docs/architecture/08-deployment.md#配置): read the
// configuration the way serve does, print every setting with its value and where the value
// came from (secrets only as "set"), then what does not fit together. A configuration that
// does not load exits 2, like serve; one that loads but has problems exits 1.
func runConfigCheck(_ context.Context, e *env, args []string) int {
	cfg, code := loadConfig("config check", e, args, nil)
	if cfg == nil {
		return code
	}
	file := cfg.File
	if file == "" {
		file = "none (" + config.ConfigEnv + " is not set)"
	}
	fmt.Fprintf(e.stdout, "Configuration file: %s\n\n", file)
	tw := tabwriter.NewWriter(e.stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SETTING\tVALUE\tFROM")
	for _, s := range config.Settings() {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", s.Key, settingValue(cfg, s.Key), cfg.Origins[s.Key])
	}
	tw.Flush()

	findings := configFindings(cfg)
	if len(findings) == 0 {
		fmt.Fprintln(e.stdout, "\nNo problems found.")
		return exitOK
	}
	fmt.Fprintln(e.stdout)
	problems := 0
	for _, f := range findings {
		fmt.Fprintf(e.stdout, "%-8s %s\n", f.level+":", f.text)
		if f.level == levelProblem {
			problems++
		}
	}
	if problems > 0 {
		return exitFailure
	}
	return exitOK
}

// settingValue is how config check shows a setting's effective value. Secrets show only
// whether they are set; a PostgreSQL address shows without its password.
func settingValue(cfg *config.Config, key string) string {
	orNone := func(s string) string {
		if s == "" {
			return "(none)"
		}
		return s
	}
	set := func(b bool) string {
		if b {
			return "set"
		}
		return "not set"
	}
	switch key {
	case "base_url":
		return cfg.BaseURL.String()
	case "listen":
		return cfg.Listen
	case "data_dir":
		return cfg.DataDir
	case "database_url":
		if cfg.DatabaseScheme() == config.SchemeSQLite {
			return cfg.DatabaseURL
		}
		if u, err := url.Parse(cfg.DatabaseURL); err == nil {
			if _, has := u.User.Password(); has {
				u.User = url.UserPassword(u.User.Username(), "xxxxx")
			}
			q := u.Query()
			if q.Has("password") {
				q.Set("password", "xxxxx")
				u.RawQuery = q.Encode()
			}
			return u.String()
		}
		return "set"
	case "master_key_file":
		if cfg.MasterKey != nil {
			return "(not used: the key is given directly)"
		}
		return cfg.MasterKeyFile
	case "master_key":
		return set(cfg.MasterKey != nil)
	case "setup_token":
		if cfg.SetupToken == "" {
			return "not set (generated on first start)"
		}
		return "set"
	case "auto_migrate":
		return strconv.FormatBool(cfg.AutoMigrate)
	case "tls.cert_file":
		return orNone(cfg.TLS.CertFile)
	case "tls.key_file":
		return orNone(cfg.TLS.KeyFile)
	case "tls.acme_domains":
		return orNone(strings.Join(cfg.TLS.ACMEDomains, ","))
	case "tls.acme_email":
		return orNone(cfg.TLS.ACMEEmail)
	case "tls.acme_directory_url":
		return orNone(cfg.TLS.ACMEDirectoryURL)
	case "tls.http_redirect_listen":
		return orNone(cfg.TLS.HTTPRedirectListen)
	case "trusted_proxies":
		parts := make([]string, len(cfg.TrustedProxies))
		for i, p := range cfg.TrustedProxies {
			parts[i] = p.String()
		}
		return orNone(strings.Join(parts, ","))
	case "insecure_http":
		return strconv.FormatBool(cfg.InsecureHTTP)
	case "default_locale":
		return cfg.DefaultLocale
	case "log.level":
		return cfg.LogLevel
	case "log.format":
		return cfg.LogFormat
	case "metrics_addr":
		if cfg.MetricsAddr == "" {
			return "off"
		}
		return cfg.MetricsAddr
	case "git":
		return cfg.Git
	case "source_sync_interval":
		return cfg.SourceSyncInterval.String()
	case "max_bundle_mb":
		return strconv.Itoa(cfg.MaxBundleMB)
	case "cli_dist_dir":
		// Unless one is configured, two directories are searched in turn.
		return strings.Join(cfg.CLIDistDirs, ", then ")
	case "cli_download_base":
		return orNone(cfg.CLIDownloadBase)
	case "backup.dir":
		if cfg.BackupDir == "" {
			return "off"
		}
		return cfg.BackupDir
	case "backup.keep":
		return strconv.Itoa(cfg.BackupKeep)
	case "web_dev_proxy":
		if cfg.WebDevProxy == nil {
			return "(none)"
		}
		return cfg.WebDevProxy.String()
	case "modules_disabled":
		return orNone(strings.Join(cfg.ModulesDisabled, ","))
	}
	return "?"
}

// The levels of a finding: a problem is a contradiction that makes the server misbehave; a
// note is worth knowing but may be intended.
const (
	levelProblem = "problem"
	levelNote    = "note"
)

type finding struct{ level, text string }

// configFindings are the contradictions and surprises in a configuration that loaded. What
// cannot work at all is already refused by config.Load; this is what serve would accept.
func configFindings(cfg *config.Config) []finding {
	var out []finding
	add := func(level, format string, args ...any) {
		out = append(out, finding{level, fmt.Sprintf(format, args...)})
	}
	https := cfg.BaseURL.Scheme == "https"
	proxy := len(cfg.TrustedProxies) > 0
	listenHost, listenPort, _ := net.SplitHostPort(cfg.Listen)

	switch {
	case https && !cfg.ServesTLS() && !proxy:
		add(levelProblem, "base_url is https://, but this server does not serve HTTPS and no reverse proxy is declared: set %s to the proxy's address range, or give a certificate (%s)",
			config.EnvName("TRUSTED_PROXIES"), config.EnvName("TLS_CERT_FILE"))
	case !https && cfg.ServesTLS():
		add(levelProblem, "this server serves HTTPS, but base_url is http://: links, sign-in callbacks and cookies would use plain http; make base_url https://")
	case !https && proxy:
		// config.Load refuses plain http on a network address, so base_url is on this machine
		// here: a trial, such as Compose (which always declares its network as the proxy)
		// opened at http://localhost. Worth knowing, not a contradiction.
		add(levelNote, "base_url is http:// on this machine while a reverse proxy is declared (trusted_proxies): fine for a trial on this computer; for the team, let the proxy serve HTTPS and make base_url its https:// address")
	}
	if !proxy && !cfg.ServesTLS() && config.IsLoopbackHost(cfg.BaseURL.Hostname()) {
		if port := cfg.BaseURL.Port(); port != "" && port != listenPort {
			add(levelNote, "base_url is on this machine at port %s, but the server listens on %s: links point to a port nothing serves unless something forwards it", port, cfg.Listen)
		}
	}
	if proxy && !config.IsLoopbackHost(listenHost) && !inContainer() {
		add(levelNote, "behind a reverse proxy, but listening on %s: anyone who can reach that port bypasses the proxy; listen on 127.0.0.1 if the proxy is on this machine", cfg.Listen)
	}
	if len(cfg.TLS.ACMEDomains) > 0 {
		if listenPort != "443" {
			add(levelNote, "automatic certificates are checked by the authority on port 443 of %s (TLS-ALPN-01); the server listens on port %s, so 443 must be forwarded to it",
				strings.Join(cfg.TLS.ACMEDomains, ", "), listenPort)
		}
		if cfg.TLS.ACMEEmail == "" {
			add(levelNote, "tls.acme_email is not set: the certificate authority cannot warn anyone about problems with the certificates")
		}
	}
	if cfg.InsecureHTTP {
		add(levelNote, "insecure_http is on: personal secrets may travel over plain HTTP; only for an isolated trial")
	}
	if cfg.BackupDir != "" && cfg.DatabaseScheme() != config.SchemeSQLite {
		add(levelProblem, "backup.dir is set, but the built-in backup is for SQLite only; back PostgreSQL up with pg_dump or your provider's snapshots")
	}
	if cfg.MasterKey == nil && within(cfg.MasterKeyFile, cfg.DataDir) {
		add(levelNote, "the master key file is in the data directory: a copy of the whole directory can read every secret; keep the key's backup apart from the database's")
	}
	if cfg.BackupDir != "" && cfg.MasterKey == nil && within(cfg.MasterKeyFile, cfg.BackupDir) {
		add(levelProblem, "the master key file is inside backup.dir: the backups would sit next to the key that opens them")
	}
	if cfg.MetricsAddr != "" {
		if host, _, _ := net.SplitHostPort(cfg.MetricsAddr); !config.IsLoopbackHost(host) {
			add(levelNote, "metrics are served on %s, reachable from the network; they carry no secrets but do describe the server", cfg.MetricsAddr)
		}
	}
	if cfg.WebDevProxy != nil {
		add(levelNote, "web_dev_proxy is set: the console comes from a development server, not from the program (development only)")
	}
	return out
}

// inContainer reports whether the server runs in a container (Docker writes /.dockerenv, Podman
// /run/.containerenv). There it has to listen on every interface of the container, and who can
// reach the port is decided by how the port is published, not by the listen address. A variable
// for the tests.
var inContainer = func() bool {
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	return false
}

// within reports whether path is inside dir (or is dir).
func within(path, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

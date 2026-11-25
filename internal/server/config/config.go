// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
)

// Config is the startup configuration of the server: what has to be known
// before the process can open its database. Everything that can change at
// run time (sign-in methods, mail server, resource repositories…) lives in
// the database instead, so that it can be edited on the web, is audited and
// is shared between instances.
//
// The table of settings, their variables, file keys and defaults is
// docs/architecture/08-deployment.md#配置 in the design repository; fields
// below are in the same order.
type Config struct {
	// File is the configuration file that was read, or "".
	File string

	BaseURL       *url.URL // public address, without a trailing slash
	Listen        string   // host:port
	DataDir       string
	DatabaseURL   string // secret: may hold a password
	MasterKeyFile string
	MasterKey     []byte // secret: 32 bytes when given directly, else nil
	SetupToken    string // secret: "" when it is to be generated on first start
	AutoMigrate   bool
	TLS           TLS
	// TrustedProxies are the address ranges of reverse proxies. Setting them
	// declares that the server runs behind a proxy that terminates TLS.
	TrustedProxies     []netip.Prefix
	InsecureHTTP       bool
	DefaultLocale      string
	LogLevel           string // validated by logging.ParseLevel
	LogFormat          string // validated by logging.ParseFormat
	MetricsAddr        string // "" when metrics are off
	Git                string
	SourceSyncInterval time.Duration
	MaxBundleMB        int
	// CLIDistDir is the directory with the release archives of the
	// command-line program that the server hands out (the configured value,
	// or the default). CLIDistDirs are the directories searched, in order:
	// only CLIDistDir when it is configured, otherwise <data_dir>/cli
	// first (where an administrator can put the archives of a container or
	// a single binary), then the default that the packages and the image
	// fill.
	CLIDistDir  string
	CLIDistDirs []string
	// CLIDownloadBase is the URL of a directory with a release's
	// checksums.txt and archives (a mirror or an internal artifact store).
	// The install scripts the server hands out download from there when
	// none of CLIDistDirs holds the archives. "" when not set: there is no
	// default, so that nothing points at the internet unless asked to.
	CLIDownloadBase string
	BackupDir       string // "" when the built-in backup is off
	BackupKeep      int
	ModulesDisabled []string
	// WebDevProxy is the console's development server (Vite) on this machine:
	// when set, the console is not served from the files built into the
	// program but proxied from there. nil in production.
	WebDevProxy *url.URL

	// Origins tells, for each setting (by its file key), where its value came
	// from. "config check" prints it.
	Origins map[string]Origin
}

// TLS is how the server terminates TLS itself, when it does.
type TLS struct {
	CertFile           string
	KeyFile            string
	ACMEDomains        []string
	ACMEEmail          string
	HTTPRedirectListen string
}

// Enabled reports whether the server itself serves HTTPS.
func (t TLS) Enabled() bool {
	return t.CertFile != "" || len(t.ACMEDomains) > 0
}

// Origin is where the value of one setting came from.
type Origin struct {
	Kind   OriginKind
	Detail string // the variable, flag or file the value was read from
}

// OriginKind is one of the four sources, in increasing precedence.
type OriginKind string

// The sources of a setting, from the lowest precedence to the highest.
const (
	FromDefault OriginKind = "default"
	FromFile    OriginKind = "file"
	FromEnv     OriginKind = "env"
	FromFlag    OriginKind = "flag"
)

func (o Origin) String() string {
	if o.Detail == "" {
		return string(o.Kind)
	}
	return string(o.Kind) + " " + o.Detail
}

// EnvName returns the environment variable of a setting, given the rest of
// its name ("BASE_URL" -> "RGD_BASE_URL").
func EnvName(suffix string) string { return brand.EnvPrefix + suffix }

// Database kinds, by the scheme of DatabaseURL.
const (
	SchemeSQLite   = "sqlite"
	SchemePostgres = "postgres"
)

// DatabaseScheme returns SchemeSQLite or SchemePostgres.
func (c *Config) DatabaseScheme() string {
	if strings.HasPrefix(c.DatabaseURL, SchemeSQLite+":") {
		return SchemeSQLite
	}
	return SchemePostgres
}

// ServesTLS reports whether the server terminates TLS on its listener.
func (c *Config) ServesTLS() bool { return c.TLS.Enabled() }

// The names that defaults are made from.
var (
	defaultDataDir = "/var/lib/" + brand.CLILong
	defaultDBName  = brand.CLILong + ".db"
	defaultKeyName = "master.key"
	defaultCLIDist = "/usr/share/" + brand.CLILong + "/cli"
	// defaultCLIDistInData is searched before defaultCLIDist, in the data
	// directory.
	defaultCLIDistInData = "cli"
	defaultListen        = "127.0.0.1:8080"
	defaultLocale        = "en"
	defaultInterval      = 10 * time.Minute
)

// apply parses the raw values into c. Each value is a string; lists are
// comma-separated. Errors name the setting the way it was given.
func (c *Config) apply(raw map[string]rawValue) error {
	var errs []error
	get := func(key string) (string, bool) {
		v, ok := raw[key]
		return v.value, ok
	}
	fail := func(key string, format string, args ...any) {
		where := raw[key].origin.String()
		errs = append(errs, fmt.Errorf("%s (%s): %s", key, where, fmt.Sprintf(format, args...)))
	}

	// base_url
	if v, ok := get("base_url"); !ok {
		errs = append(errs, fmt.Errorf("base_url is required: set %s to the public address of this server, such as https://hub.example.com", EnvName("BASE_URL")))
	} else if u, err := parseBaseURL(v); err != nil {
		fail("base_url", "%v", err)
	} else {
		c.BaseURL = u
	}

	c.Listen = defaultListen
	if v, ok := get("listen"); ok {
		if _, _, err := net.SplitHostPort(v); err != nil {
			fail("listen", "want host:port, such as 127.0.0.1:8080: %v", err)
		} else {
			c.Listen = v
		}
	}

	c.DataDir = defaultDataDir
	if v, ok := get("data_dir"); ok {
		c.DataDir = v
	}
	c.DataDir = filepath.Clean(c.DataDir)

	c.DatabaseURL = SchemeSQLite + "://" + filepath.ToSlash(filepath.Join(c.DataDir, defaultDBName))
	if v, ok := get("database_url"); ok {
		if err := checkDatabaseURL(v); err != nil {
			fail("database_url", "%v", err)
		} else {
			c.DatabaseURL = v
		}
	}

	c.MasterKeyFile = filepath.Join(c.DataDir, defaultKeyName)
	_, keyFileSet := get("master_key_file")
	if v, ok := get("master_key_file"); ok {
		c.MasterKeyFile = v
	}
	if v, ok := get("master_key"); ok {
		if keyFileSet {
			fail("master_key", "give the master key either directly or as a file (%s), not both", EnvName("MASTER_KEY_FILE"))
		}
		key, err := base64.StdEncoding.DecodeString(v)
		if err != nil || len(key) != 32 {
			fail("master_key", "want 32 bytes in standard base64 (44 characters)")
		} else {
			c.MasterKey = key
		}
	}

	if v, ok := get("setup_token"); ok {
		c.SetupToken = v
	}

	c.AutoMigrate = true
	if v, ok := get("auto_migrate"); ok {
		if b, err := strconv.ParseBool(v); err != nil {
			fail("auto_migrate", "want true or false")
		} else {
			c.AutoMigrate = b
		}
	}

	c.TLS.CertFile, _ = get("tls.cert_file")
	c.TLS.KeyFile, _ = get("tls.key_file")
	if (c.TLS.CertFile == "") != (c.TLS.KeyFile == "") {
		errs = append(errs, fmt.Errorf("tls.cert_file and tls.key_file go together: give both or neither"))
	}
	if v, ok := get("tls.acme_domains"); ok {
		c.TLS.ACMEDomains = splitList(v)
	}
	c.TLS.ACMEEmail, _ = get("tls.acme_email")
	if c.TLS.CertFile != "" && len(c.TLS.ACMEDomains) > 0 {
		errs = append(errs, fmt.Errorf("tls.cert_file and tls.acme_domains exclude each other: bring your own certificate or have one issued, not both"))
	}
	if c.TLS.ACMEEmail != "" && len(c.TLS.ACMEDomains) == 0 {
		errs = append(errs, fmt.Errorf("tls.acme_email needs tls.acme_domains"))
	}
	if v, ok := get("tls.http_redirect_listen"); ok {
		if _, _, err := net.SplitHostPort(v); err != nil {
			fail("tls.http_redirect_listen", "want host:port, such as :80: %v", err)
		} else if !c.TLS.Enabled() {
			fail("tls.http_redirect_listen", "redirects to HTTPS, so it needs tls.cert_file or tls.acme_domains")
		} else {
			c.TLS.HTTPRedirectListen = v
		}
	}

	if v, ok := get("trusted_proxies"); ok {
		for _, item := range splitList(v) {
			p, err := parsePrefix(item)
			if err != nil {
				fail("trusted_proxies", "%q is not an address or address range: %v", item, err)
				continue
			}
			c.TrustedProxies = append(c.TrustedProxies, p)
		}
	}

	if v, ok := get("insecure_http"); ok {
		if b, err := strconv.ParseBool(v); err != nil {
			fail("insecure_http", "want true or false")
		} else {
			c.InsecureHTTP = b
		}
	}

	c.DefaultLocale = defaultLocale
	if v, ok := get("default_locale"); ok {
		if !localeTag.MatchString(v) {
			fail("default_locale", "%q is not a language tag such as en or zh-CN", v)
		} else {
			c.DefaultLocale = v
		}
	}

	c.LogLevel = "info"
	if v, ok := get("log.level"); ok {
		switch strings.ToLower(v) {
		case "debug", "info", "warn", "error":
			c.LogLevel = strings.ToLower(v)
		default:
			fail("log.level", "want debug, info, warn or error")
		}
	}
	c.LogFormat = "json"
	if v, ok := get("log.format"); ok {
		switch strings.ToLower(v) {
		case "json", "text":
			c.LogFormat = strings.ToLower(v)
		default:
			fail("log.format", "want json or text")
		}
	}

	if v, ok := get("metrics_addr"); ok {
		if _, _, err := net.SplitHostPort(v); err != nil {
			fail("metrics_addr", "want host:port, such as 127.0.0.1:9464: %v", err)
		} else if v == c.Listen {
			fail("metrics_addr", "must not be the address the server listens on; metrics stay off the public port")
		} else {
			c.MetricsAddr = v
		}
	}

	c.Git = "git"
	if v, ok := get("git"); ok {
		c.Git = v
	}

	c.SourceSyncInterval = defaultInterval
	if v, ok := get("source_sync_interval"); ok {
		if d, err := time.ParseDuration(v); err != nil || d < time.Minute {
			fail("source_sync_interval", "want a duration of at least 1m, such as 10m")
		} else {
			c.SourceSyncInterval = d
		}
	}

	c.MaxBundleMB = 5
	if v, ok := get("max_bundle_mb"); ok {
		if n, err := strconv.Atoi(v); err != nil || n < 1 || n > 1024 {
			fail("max_bundle_mb", "want a whole number of megabytes between 1 and 1024")
		} else {
			c.MaxBundleMB = n
		}
	}

	c.CLIDistDir = defaultCLIDist
	c.CLIDistDirs = []string{filepath.Join(c.DataDir, defaultCLIDistInData), defaultCLIDist}
	if v, ok := get("cli_dist_dir"); ok {
		c.CLIDistDir = v
		c.CLIDistDirs = []string{v}
	}
	if v, ok := get("cli_download_base"); ok {
		u, err := url.Parse(v)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			fail("cli_download_base", "want an https:// address")
		} else {
			c.CLIDownloadBase = strings.TrimRight(v, "/")
		}
	}

	if v, ok := get("backup.dir"); ok {
		c.BackupDir = v
	}
	c.BackupKeep = 14
	if v, ok := get("backup.keep"); ok {
		if n, err := strconv.Atoi(v); err != nil || n < 1 {
			fail("backup.keep", "want a whole number of at least 1")
		} else {
			c.BackupKeep = n
		}
	}

	if v, ok := get("modules_disabled"); ok {
		for _, id := range splitList(v) {
			if !moduleID.MatchString(id) {
				fail("modules_disabled", "%q is not a module identifier (lower case letters, digits and dashes)", id)
				continue
			}
			c.ModulesDisabled = append(c.ModulesDisabled, id)
		}
	}

	if v, ok := get("web_dev_proxy"); ok {
		u, err := url.Parse(v)
		switch {
		case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/"):
			fail("web_dev_proxy", "want the address of the development server, such as http://127.0.0.1:5173")
		case !IsLoopbackHost(u.Hostname()):
			fail("web_dev_proxy", "the development server must be on this machine (localhost or a loopback address)")
		default:
			u.Path = ""
			c.WebDevProxy = u
		}
	}

	if len(errs) == 0 {
		if err := c.checkTransport(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

var (
	localeTag = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)
	moduleID  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

func parseBaseURL(v string) (*url.URL, error) {
	u, err := url.Parse(v)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" && u.Scheme != "http" || u.Host == "" {
		return nil, fmt.Errorf("want an absolute http:// or https:// address, such as https://hub.example.com")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("want only a scheme, a host and an optional path")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

func checkDatabaseURL(v string) error {
	switch {
	case strings.HasPrefix(v, SchemeSQLite+"://"):
		if strings.TrimPrefix(v, SchemeSQLite+"://") == "" {
			return errors.New("sqlite:// needs the path of the database file")
		}
		return nil
	case strings.HasPrefix(v, SchemePostgres+"://"), strings.HasPrefix(v, "postgresql://"):
		u, err := url.Parse(v)
		if err != nil {
			// The error text would repeat the URL, password and all.
			return errors.New("not a valid postgres:// address")
		}
		if u.Host == "" && u.Query().Get("host") == "" {
			return errors.New("postgres:// needs a host")
		}
		return nil
	}
	return errors.New("want sqlite://<path> or postgres://<user>@<host>/<database>")
}

func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		return p.Masked(), err
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

func splitList(v string) []string {
	var out []string
	for item := range strings.SplitSeq(v, ",") {
		if item = strings.TrimSpace(item); item != "" && !slices.Contains(out, item) {
			out = append(out, item)
		}
	}
	return out
}

// IsLoopbackHost reports whether host (a name or an address, without a port)
// is this machine. An empty host and the unspecified addresses (0.0.0.0,
// ::) mean every interface, which is not loopback.
func IsLoopbackHost(host string) bool {
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.IsLoopback()
}

// ErrPlaintext is wrapped by the error Load returns when the configuration
// would serve plain HTTP to the network.
var ErrPlaintext = errors.New("refusing to serve plain HTTP beyond this machine")

// checkTransport is the rule of docs/architecture/06-server.md#安全基线:
// personal secrets travel over this connection, so plain HTTP is only for
// this machine. Three ways out: serve TLS (a certificate or ACME); declare a
// reverse proxy that terminates TLS (trusted_proxies, with an https base
// URL); or say explicitly that this is an isolated trial (insecure_http).
func (c *Config) checkTransport() error {
	if c.InsecureHTTP || c.ServesTLS() {
		return nil
	}
	behindProxy := len(c.TrustedProxies) > 0
	if c.BaseURL.Scheme == "http" && !IsLoopbackHost(c.BaseURL.Hostname()) {
		return fmt.Errorf("%w: base_url %s is plain http on a network address; use https:// with a reverse proxy (%s) or a certificate (%s), or for an isolated trial set %s=1",
			ErrPlaintext, c.BaseURL, EnvName("TRUSTED_PROXIES"), EnvName("TLS_CERT_FILE"), EnvName("INSECURE_HTTP"))
	}
	host, _, _ := net.SplitHostPort(c.Listen)
	if !IsLoopbackHost(host) && !behindProxy {
		return fmt.Errorf("%w: listen %s is reachable from the network without TLS; behind a reverse proxy set %s to its address range, or give a certificate (%s), or for an isolated trial set %s=1",
			ErrPlaintext, c.Listen, EnvName("TRUSTED_PROXIES"), EnvName("TLS_CERT_FILE"), EnvName("INSECURE_HTTP"))
	}
	return nil
}

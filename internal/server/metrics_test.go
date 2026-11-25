// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/server/config"
)

// A minimal check of the Prometheus text format (version 0.0.4), strict where the format is:
// what this file accepts, Prometheus parses. It does not use Prometheus's own parser, which
// would be a dependency.

type promSample struct {
	name   string
	labels map[string]string
	value  float64
}

type promFamily struct {
	typ     string
	samples []promSample
}

var (
	promMetricName = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	promLabelName  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	promTypes      = []string{"counter", "gauge", "histogram", "summary", "untyped"}
)

// parseProm parses an exposition and checks the rules Prometheus enforces: names, label syntax
// and escapes, values, a TYPE before a family's samples and at most once, each family's samples
// together, no series twice, and for histograms cumulative buckets with +Inf equal to _count.
func parseProm(text string) (map[string]*promFamily, error) {
	if text != "" && !strings.HasSuffix(text, "\n") {
		return nil, errors.New("the last line does not end with a line feed")
	}
	fams := map[string]*promFamily{}
	var order []string // families in the order their samples appear
	seen := map[string]bool{}
	for n, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		fail := func(format string, args ...any) (map[string]*promFamily, error) {
			return nil, fmt.Errorf("line %d %q: %s", n+1, line, fmt.Sprintf(format, args...))
		}
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "# HELP ") || strings.HasPrefix(line, "# TYPE "):
			fields := strings.SplitN(line[len("# HELP "):], " ", 2)
			name := fields[0]
			if !promMetricName.MatchString(name) {
				return fail("bad metric name")
			}
			if strings.HasPrefix(line, "# HELP ") {
				if len(fields) == 2 && strings.Contains(strings.NewReplacer(`\\`, "", `\n`, "").Replace(fields[1]), `\`) {
					return fail("bad escape in HELP")
				}
				continue
			}
			if len(fields) != 2 || !slices.Contains(promTypes, fields[1]) {
				return fail("bad TYPE")
			}
			if _, dup := fams[name]; dup {
				return fail("second TYPE for %s, or TYPE after its samples", name)
			}
			fams[name] = &promFamily{typ: fields[1]}
		case strings.HasPrefix(line, "#"):
			continue
		default:
			s, err := parsePromSample(line)
			if err != nil {
				return fail("%v", err)
			}
			fam := s.name
			for _, suffix := range []string{"_bucket", "_sum", "_count"} {
				if base, ok := strings.CutSuffix(s.name, suffix); ok && fams[base] != nil && fams[base].typ == "histogram" {
					fam = base
				}
			}
			f := fams[fam]
			if f == nil {
				return fail("no TYPE for %s before its samples", fam)
			}
			if f.typ == "histogram" && fam == s.name {
				return fail("a histogram sample without _bucket, _sum or _count")
			}
			if len(order) == 0 || order[len(order)-1] != fam {
				if slices.Contains(order, fam) {
					return fail("the samples of %s are not together", fam)
				}
				order = append(order, fam)
			}
			key := s.name + "{" + labelKey(s.labels, "") + "}"
			if seen[key] {
				return fail("the series %s appears twice", key)
			}
			seen[key] = true
			if f.typ == "counter" && (s.value < 0 || math.IsNaN(s.value)) {
				return fail("a counter below zero")
			}
			f.samples = append(f.samples, s)
		}
	}
	for name, f := range fams {
		if f.typ == "histogram" {
			if err := checkHistogram(name, f); err != nil {
				return nil, err
			}
		}
	}
	return fams, nil
}

func parsePromSample(line string) (promSample, error) {
	s := promSample{labels: map[string]string{}}
	i := strings.IndexAny(line, "{ ")
	if i < 0 {
		return s, errors.New("no value")
	}
	s.name, line = line[:i], line[i:]
	if !promMetricName.MatchString(s.name) {
		return s, errors.New("bad metric name")
	}
	if strings.HasPrefix(line, "{") {
		line = line[1:]
		for !strings.HasPrefix(line, "}") {
			eq := strings.Index(line, `="`)
			if eq < 0 {
				return s, errors.New(`a label without ="`)
			}
			name := line[:eq]
			if !promLabelName.MatchString(name) || strings.HasPrefix(name, "__") {
				return s, fmt.Errorf("bad label name %q", name)
			}
			if _, dup := s.labels[name]; dup {
				return s, fmt.Errorf("label %q twice", name)
			}
			line = line[eq+2:]
			var v strings.Builder
			for {
				if line == "" {
					return s, errors.New("unterminated label value")
				}
				c := line[0]
				line = line[1:]
				if c == '"' {
					break
				}
				if c == '\n' {
					return s, errors.New("a line feed in a label value")
				}
				if c == '\\' {
					if line == "" {
						return s, errors.New("unterminated escape")
					}
					switch line[0] {
					case '\\':
						v.WriteByte('\\')
					case '"':
						v.WriteByte('"')
					case 'n':
						v.WriteByte('\n')
					default:
						return s, fmt.Errorf(`bad escape \%c`, line[0])
					}
					line = line[1:]
					continue
				}
				v.WriteByte(c)
			}
			s.labels[name] = v.String()
			if rest, ok := strings.CutPrefix(line, ","); ok {
				line = rest
			} else if !strings.HasPrefix(line, "}") {
				return s, errors.New("labels not separated by commas")
			}
		}
		line = line[1:]
	}
	fields := strings.Split(strings.TrimPrefix(line, " "), " ")
	if !strings.HasPrefix(line, " ") || len(fields) < 1 || len(fields) > 2 {
		return s, errors.New("not \"<name>[{labels}] <value> [<timestamp>]\"")
	}
	v, err := parsePromValue(fields[0])
	if err != nil {
		return s, err
	}
	s.value = v
	if len(fields) == 2 {
		if _, err := strconv.ParseInt(fields[1], 10, 64); err != nil {
			return s, fmt.Errorf("bad timestamp %q", fields[1])
		}
	}
	return s, nil
}

func parsePromValue(v string) (float64, error) {
	switch v {
	case "+Inf":
		return math.Inf(1), nil
	case "-Inf":
		return math.Inf(-1), nil
	case "NaN":
		return math.NaN(), nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || strings.ContainsAny(v, "_xXpP") || strings.EqualFold(v, "inf") || strings.EqualFold(v, "infinity") {
		return 0, fmt.Errorf("bad value %q", v)
	}
	return f, nil
}

func labelKey(labels map[string]string, without string) string {
	var parts []string
	for k, v := range labels {
		if k != without {
			parts = append(parts, k+"="+strconv.Quote(v))
		}
	}
	slices.Sort(parts)
	return strings.Join(parts, ",")
}

func checkHistogram(name string, f *promFamily) error {
	type series struct {
		bounds     []float64
		counts     []float64
		sum, count *float64
	}
	all := map[string]*series{}
	get := func(key string) *series {
		if all[key] == nil {
			all[key] = &series{}
		}
		return all[key]
	}
	for _, s := range f.samples {
		key := labelKey(s.labels, "le")
		switch s.name {
		case name + "_bucket":
			le, ok := s.labels["le"]
			if !ok {
				return fmt.Errorf("%s: a bucket without le", name)
			}
			b, err := parsePromValue(le)
			if err != nil {
				return fmt.Errorf("%s: le %q: %v", name, le, err)
			}
			sr := get(key)
			if n := len(sr.bounds); n > 0 && b <= sr.bounds[n-1] {
				return fmt.Errorf("%s{%s}: buckets not in increasing order of le", name, key)
			}
			if n := len(sr.counts); n > 0 && s.value < sr.counts[n-1] {
				return fmt.Errorf("%s{%s}: buckets are not cumulative", name, key)
			}
			sr.bounds, sr.counts = append(sr.bounds, b), append(sr.counts, s.value)
		case name + "_sum":
			get(key).sum = &s.value
		case name + "_count":
			get(key).count = &s.value
		}
	}
	for key, sr := range all {
		n := len(sr.bounds)
		switch {
		case n == 0 || !math.IsInf(sr.bounds[n-1], 1):
			return fmt.Errorf("%s{%s}: no +Inf bucket", name, key)
		case sr.count == nil || sr.sum == nil:
			return fmt.Errorf("%s{%s}: no _count or _sum", name, key)
		case *sr.count != sr.counts[n-1]:
			return fmt.Errorf("%s{%s}: _count %v is not the +Inf bucket %v", name, key, *sr.count, sr.counts[n-1])
		}
	}
	return nil
}

// TestPromParser: the checker refuses what Prometheus refuses, so that its passing means
// something.
func TestPromParser(t *testing.T) {
	good := "# HELP a_total A.\n# TYPE a_total counter\na_total{x=\"1\",y=\"q\\\"\\\\\\n\"} 3\na_total 4 1700000000000\n" +
		"# TYPE h histogram\nh_bucket{le=\"0.1\"} 1\nh_bucket{le=\"+Inf\"} 2\nh_sum 0.3\nh_count 2\n"
	fams, err := parseProm(good)
	if err != nil {
		t.Fatal(err)
	}
	if got := fams["a_total"].samples[0].labels["y"]; got != "q\"\\\n" {
		t.Errorf("unescaped %q", got)
	}
	for name, bad := range map[string]string{
		"no line feed at the end":   "# TYPE a gauge\na 1",
		"no TYPE":                   "a 1\n",
		"TYPE twice":                "# TYPE a gauge\n# TYPE a gauge\na 1\n",
		"bad type":                  "# TYPE a number\na 1\n",
		"bad name":                  "# TYPE 1a gauge\n1a 1\n",
		"bad label name":            "# TYPE a gauge\na{1x=\"1\"} 1\n",
		"unquoted label":            "# TYPE a gauge\na{x=1} 1\n",
		"bad escape":                "# TYPE a gauge\na{x=\"\\t\"} 1\n",
		"no comma":                  "# TYPE a gauge\na{x=\"1\" y=\"2\"} 1\n",
		"bad value":                 "# TYPE a gauge\na 1,5\n",
		"two spaces":                "# TYPE a gauge\na  1\n",
		"series twice":              "# TYPE a gauge\na{x=\"1\"} 1\na{x=\"1\"} 2\n",
		"family split":              "# TYPE a gauge\n# TYPE b gauge\na 1\nb 1\na{x=\"1\"} 1\n",
		"negative counter":          "# TYPE a_total counter\na_total -1\n",
		"buckets not cumulative":    "# TYPE h histogram\nh_bucket{le=\"1\"} 2\nh_bucket{le=\"+Inf\"} 1\nh_sum 1\nh_count 1\n",
		"no +Inf bucket":            "# TYPE h histogram\nh_bucket{le=\"1\"} 1\nh_sum 1\nh_count 1\n",
		"count is not +Inf":         "# TYPE h histogram\nh_bucket{le=\"+Inf\"} 2\nh_sum 1\nh_count 1\n",
		"histogram without suffix":  "# TYPE h histogram\nh 1\n",
		"label value with new line": "# TYPE a gauge\na{x=\"1\n\"} 1\n",
	} {
		if _, err := parseProm(bad); err == nil {
			t.Errorf("%s: accepted %q", name, bad)
		}
	}
}

// TestPromWriterEscapes: label values and help texts with the characters the format escapes come
// out parseable and read back as they were.
func TestPromWriterEscapes(t *testing.T) {
	var b strings.Builder
	value := "GET /a\\b \"c\"\nd"
	promWriter{&b}.family("x", "gauge", "Help with \\ and\nline feed.", []sample{{labels: []string{"route", value}, value: 1}})
	fams, err := parseProm(b.String())
	if err != nil {
		t.Fatalf("%v\n%s", err, b.String())
	}
	if got := fams[metricPrefix+"x"].samples[0].labels["route"]; got != value {
		t.Errorf("read back %q", got)
	}
}

// TestMetricsEndpoint: with metrics_addr set, the server serves /metrics on that address only,
// in a format Prometheus parses, with requests by route and status, jobs, the outbox and the
// database pools.
func TestMetricsEndpoint(t *testing.T) {
	e, stdout, _ := testEnv(t, map[string]string{config.EnvName("METRICS_ADDR"): "localhost:0"})
	metricsAt := make(chan string, 1)
	e.metricsListening = metricsAt
	srv := startServe(t, e)
	var addr string
	select {
	case addr = <-metricsAt:
	case <-time.After(10 * time.Second):
		t.Fatal("no metrics listener")
	}
	for _, path := range []string{"/healthz", "/healthz", "/readyz", "/api/v1/settings", "/api/v1/no-such-thing"} {
		get(t, "http://"+srv.addr+path)
	}
	if resp, _ := get(t, "http://"+srv.addr+"/metrics"); resp.StatusCode == http.StatusOK && strings.Contains(resp.Header.Get("Content-Type"), "version=0.0.4") {
		t.Error("the public port serves the metrics")
	}

	// The jobs run once at start; wait for the audit check's outcome (other jobs, such as
	// source-sync, may finish first).
	var fams map[string]*promFamily
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + "/metrics")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/plain; version=0.0.4; charset=utf-8" {
			t.Fatalf("/metrics: %s %v", resp.Status, resp.Header)
		}
		if fams, err = parseProm(string(body)); err != nil {
			t.Fatalf("not the Prometheus text format: %v\n%s", err, body)
		}
		if f := fams[metricPrefix+"job_runs_total"]; f != nil && slices.ContainsFunc(f.samples, func(s promSample) bool {
			return s.labels["job"] == auditCheckJob
		}) || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	value := func(family, name string, labels map[string]string) (float64, bool) {
		f := fams[metricPrefix+family]
		if f == nil {
			return 0, false
		}
		for _, s := range f.samples {
			if s.name == metricPrefix+name && labelKey(s.labels, "") == labelKey(labels, "") {
				return s.value, true
			}
		}
		return 0, false
	}
	if v, ok := value("http_requests_total", "http_requests_total", map[string]string{"route": "GET /healthz", "code": "200"}); !ok || v < 2 {
		t.Errorf("requests to /healthz: %v %v", v, ok)
	}
	if v, ok := value("http_requests_total", "http_requests_total", map[string]string{"route": "GET /api/v1/settings", "code": "401"}); !ok || v != 1 {
		t.Errorf("requests to an API operation, by its template: %v %v", v, ok)
	}
	if f := fams[metricPrefix+"http_request_duration_seconds"]; f == nil || f.typ != "histogram" {
		t.Error("no request duration histogram")
	}
	for _, s := range fams[metricPrefix+"http_requests_total"].samples {
		if strings.Contains(s.labels["route"], "no-such-thing") {
			t.Errorf("a real path became a label: %v", s.labels)
		}
	}
	if v, ok := value("job_runs_total", "job_runs_total", map[string]string{"job": auditCheckJob, "result": "ok"}); !ok || v < 1 {
		t.Errorf("job runs: %v %v", v, ok)
	}
	if _, ok := value("mail_outbox_messages", "mail_outbox_messages", map[string]string{"state": "queued"}); !ok {
		t.Error("no outbox backlog")
	}
	for _, pool := range []string{"write", "read"} {
		if v, ok := value("db_max_open_connections", "db_max_open_connections", map[string]string{"pool": pool}); !ok || v < 1 {
			t.Errorf("pool %s: %v %v", pool, v, ok)
		}
	}
	if !strings.Contains(stdout.String(), "serving metrics") {
		t.Errorf("the log does not say where metrics are served:\n%s", stdout)
	}
}

// TestMetricsOffByDefault: without metrics_addr there is no metrics listener.
func TestMetricsOffByDefault(t *testing.T) {
	e, stdout, _ := testEnv(t, nil)
	metricsAt := make(chan string, 1)
	e.metricsListening = metricsAt
	srv := startServe(t, e)
	get(t, "http://"+srv.addr+"/healthz")
	select {
	case addr := <-metricsAt:
		t.Errorf("metrics served at %s", addr)
	default:
	}
	if strings.Contains(stdout.String(), "serving metrics") {
		t.Error("metrics are on by default")
	}
}

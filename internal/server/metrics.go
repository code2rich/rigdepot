// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"bufio"
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/buildinfo"
	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// Metrics in the Prometheus text format (version 0.0.4), written by hand: counters, gauges and
// one histogram do not need a client library (docs/architecture/06-server.md, observability).
// They are served on a listener of their own (metrics_addr), off by default and never on the
// public port.
//
// Labels have bounded values only: route templates (never real paths), status codes, job names
// and results, pool names. Nothing about people, resources or content.

// metricPrefix starts every metric name: the server's name in the form a metric name allows.
var metricPrefix = strings.ReplaceAll(brand.Server, "-", "_") + "_"

// requestBuckets are the upper bounds of the request duration histogram, in seconds.
var requestBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// requestMetrics counts the requests by route template and status code.
type requestMetrics struct {
	mu     sync.Mutex
	series map[requestKey]*requestSeries
}

type requestKey struct {
	route string
	code  int
}

type requestSeries struct {
	buckets []uint64 // not cumulative: buckets[i] counts durations in (bound i-1, bound i]
	count   uint64   // all, including those above the last bound
	sum     float64
}

func newRequestMetrics() *requestMetrics {
	return &requestMetrics{series: map[requestKey]*requestSeries{}}
}

// observe counts one request. A nil requestMetrics counts nothing.
func (m *requestMetrics) observe(route string, code int, took time.Duration) {
	if m == nil {
		return
	}
	secs := took.Seconds()
	m.mu.Lock()
	defer m.mu.Unlock()
	k := requestKey{route, code}
	s := m.series[k]
	if s == nil {
		s = &requestSeries{buckets: make([]uint64, len(requestBuckets))}
		m.series[k] = s
	}
	if i, _ := slices.BinarySearch(requestBuckets, secs); i < len(requestBuckets) {
		s.buckets[i]++
	}
	s.count++
	s.sum += secs
}

// metricsTimeout bounds the database queries of one scrape.
const metricsTimeout = 5 * time.Second

// metricsHandler serves GET /metrics.
func (a *App) metricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), metricsTimeout)
		defer cancel()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		bw := bufio.NewWriter(w)
		a.writeMetrics(ctx, bw)
		bw.Flush()
	})
	mux.HandleFunc("/", http.NotFound)
	return mux
}

// promWriter writes metric families. Each family is written whole, its samples after its HELP
// and TYPE lines.
type promWriter struct{ w io.Writer }

type sample struct {
	suffix string   // "", "_bucket", "_sum", "_count"
	labels []string // name, value, name, value…
	value  float64
}

func (p promWriter) family(name, typ, help string, samples []sample) {
	if len(samples) == 0 {
		return
	}
	name = metricPrefix + name
	fmt.Fprintf(p.w, "# HELP %s %s\n# TYPE %s %s\n", name, escapeHelp(help), name, typ)
	for _, s := range samples {
		io.WriteString(p.w, name+s.suffix)
		if len(s.labels) > 0 {
			io.WriteString(p.w, "{")
			for i := 0; i+1 < len(s.labels); i += 2 {
				if i > 0 {
					io.WriteString(p.w, ",")
				}
				fmt.Fprintf(p.w, "%s=\"%s\"", s.labels[i], escapeLabel(s.labels[i+1]))
			}
			io.WriteString(p.w, "}")
		}
		fmt.Fprintf(p.w, " %s\n", formatValue(s.value))
	}
}

func escapeHelp(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(s)
}

func escapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

func formatValue(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case math.IsNaN(v):
		return "NaN"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// writeMetrics writes every metric. What cannot be read now (the outbox, when the database does
// not answer) is left out rather than reported as zero.
func (a *App) writeMetrics(ctx context.Context, w io.Writer) {
	p := promWriter{w}
	p.family("build_info", "gauge", "The version of this server; always 1.",
		[]sample{{labels: []string{"version", buildinfo.Get().Version}, value: 1}})
	a.writeRequestMetrics(p)
	a.writeJobMetrics(p)
	a.writeOutboxMetrics(ctx, p)
	a.writePoolMetrics(p)
}

func (a *App) writeRequestMetrics(p promWriter) {
	m := a.requests
	if m == nil {
		return
	}
	m.mu.Lock()
	keys := make([]requestKey, 0, len(m.series))
	for k := range m.series {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(x, y requestKey) int { return cmp.Or(cmp.Compare(x.route, y.route), cmp.Compare(x.code, y.code)) })
	var counts, hist []sample
	for _, k := range keys {
		s := m.series[k]
		labels := []string{"route", k.route, "code", strconv.Itoa(k.code)}
		counts = append(counts, sample{labels: labels, value: float64(s.count)})
		var cum uint64
		for i, bound := range requestBuckets {
			cum += s.buckets[i]
			hist = append(hist, sample{suffix: "_bucket", labels: append(slices.Clone(labels), "le", formatValue(bound)), value: float64(cum)})
		}
		hist = append(hist,
			sample{suffix: "_bucket", labels: append(slices.Clone(labels), "le", "+Inf"), value: float64(s.count)},
			sample{suffix: "_sum", labels: labels, value: s.sum},
			sample{suffix: "_count", labels: labels, value: float64(s.count)})
	}
	m.mu.Unlock()
	p.family("http_requests_total", "counter", "HTTP requests by route template and status code.", counts)
	p.family("http_request_duration_seconds", "histogram", "How long HTTP requests took, by route template and status code.", hist)
}

func (a *App) writeJobMetrics(p promWriter) {
	if a.bg == nil || a.bg.JobCounters == nil {
		return
	}
	var runs, secs []sample
	for _, c := range a.bg.JobCounters.Snapshot() {
		labels := []string{"job", c.Job, "result", string(c.Result)}
		runs = append(runs, sample{labels: labels, value: float64(c.Runs)})
		secs = append(secs, sample{labels: labels, value: c.Seconds})
	}
	p.family("job_runs_total", "counter", "Runs of background jobs by job and result.", runs)
	p.family("job_run_seconds_total", "counter", "Time spent in background jobs by job and result.", secs)
}

func (a *App) writeOutboxMetrics(ctx context.Context, p promWriter) {
	if a.bg == nil || a.bg.Notify == nil {
		return
	}
	st, err := a.bg.Notify.Stats(ctx)
	if err != nil {
		a.log.Warn("metrics: cannot count the mail outbox", "err", err)
		return
	}
	p.family("mail_outbox_messages", "gauge", "Mail in the outbox: waiting (queued, including those between retries), retrying after a failed attempt, given up (failed).",
		[]sample{
			{labels: []string{"state", "queued"}, value: float64(st.Queued)},
			{labels: []string{"state", "retrying"}, value: float64(st.Retrying)},
			{labels: []string{"state", "failed"}, value: float64(st.Failed)},
		})
	p.family("mail_sent_last_hour", "gauge", "Mail sent in the last hour (a digest counts once).",
		[]sample{{value: float64(st.SentLastHour)}})
}

func (a *App) writePoolMetrics(p promWriter) {
	write, read := a.db.Stats()
	pools := []struct {
		name string
		st   sql.DBStats
	}{{"write", write}, {"read", read}}
	if a.db.Dialect() == dbx.Postgres {
		// One pool serves both.
		pools = pools[:1]
		pools[0].name = "shared"
	}
	var open, inUse, idle, maxOpen, waits, waited []sample
	for _, pool := range pools {
		l := []string{"pool", pool.name}
		open = append(open, sample{labels: l, value: float64(pool.st.OpenConnections)})
		inUse = append(inUse, sample{labels: l, value: float64(pool.st.InUse)})
		idle = append(idle, sample{labels: l, value: float64(pool.st.Idle)})
		maxOpen = append(maxOpen, sample{labels: l, value: float64(pool.st.MaxOpenConnections)})
		waits = append(waits, sample{labels: l, value: float64(pool.st.WaitCount)})
		waited = append(waited, sample{labels: l, value: pool.st.WaitDuration.Seconds()})
	}
	p.family("db_open_connections", "gauge", "Open database connections, by pool.", open)
	p.family("db_in_use_connections", "gauge", "Database connections in use, by pool.", inUse)
	p.family("db_idle_connections", "gauge", "Idle database connections, by pool.", idle)
	p.family("db_max_open_connections", "gauge", "The most database connections a pool opens; 0 means no limit.", maxOpen)
	p.family("db_waits_total", "counter", "Times a query waited for a free database connection, by pool.", waits)
	p.family("db_wait_seconds_total", "counter", "Time spent waiting for a free database connection, by pool.", waited)
}

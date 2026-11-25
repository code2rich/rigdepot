// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"regexp"
	"runtime/debug"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/code2rich/rigdepot/internal/logging"
	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// Job is a piece of background work run on an interval.
type Job struct {
	// Name identifies the job in the jobs table, logs and metrics: lower-case letters and
	// digits, in words separated by single dashes or dots ("mail-outbox", "insights.rollup").
	Name string
	// Every is the interval between the starts of two runs.
	Every time.Duration
	// Timeout is how long one run may take. Its context is canceled then.
	Timeout time.Duration
	// Run does the work. It must return soon after its context ends.
	Run func(ctx context.Context) error
}

var jobName = regexp.MustCompile(`^[a-z0-9]+([.-][a-z0-9]+)*$`)

// Result is the outcome of one run, as stored in jobs.last_result.
type Result string

// The outcomes of a run.
const (
	OK       Result = "ok"
	Failed   Result = "failed"   // Run returned an error or panicked
	TimedOut Result = "timeout"  // the timeout was reached
	Canceled Result = "canceled" // the server shut down or the job was removed while it ran
)

// Recorder receives the outcome of every run, for metrics.
type Recorder interface {
	JobFinished(name string, result Result, took time.Duration)
}

// Options adjust a Scheduler. The zero value is usable.
type Options struct {
	// Owner identifies this instance in leases. Empty means a new random one.
	Owner string
	// Log receives failures; nil discards them.
	Log *slog.Logger
	// Metrics receives the outcome of every run; nil records nothing.
	Metrics Recorder
	// Grace is added to a job's timeout for the length of its lease, and is how long a run that
	// ignores its canceled context is waited for. Default 30 seconds.
	Grace time.Duration
	// RetryAfter is how soon a job is tried again after the database could not be reached.
	// Default 10 seconds.
	RetryAfter time.Duration
}

// Scheduler runs jobs. Create one with New, add jobs, Start it, and Stop it on shutdown.
type Scheduler struct {
	db    *dbx.DB
	owner string
	log   *slog.Logger
	rec   Recorder
	grace time.Duration
	retry time.Duration

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	jobs    map[string]*entry
	started bool
	stopped bool
	wg      sync.WaitGroup
}

type entry struct {
	job     Job
	trigger chan struct{}
	stop    chan struct{}
	done    chan struct{}
	ctx     context.Context // ends when the job is removed or the scheduler stops
	cancel  context.CancelFunc
}

// New returns a scheduler that keeps its leases and results in db.
func New(db *dbx.DB, opts Options) *Scheduler {
	if opts.Owner == "" {
		opts.Owner = dbx.NewID()
	}
	if opts.Log == nil {
		opts.Log = logging.Discard()
	}
	if opts.Grace <= 0 {
		opts.Grace = 30 * time.Second
	}
	if opts.RetryAfter <= 0 {
		opts.RetryAfter = 10 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		db: db, owner: opts.Owner, log: opts.Log, rec: opts.Metrics, grace: opts.Grace, retry: opts.RetryAfter,
		ctx: ctx, cancel: cancel, jobs: map[string]*entry{},
	}
}

// Owner returns the identifier of this instance in leases.
func (s *Scheduler) Owner() string { return s.owner }

// Add adds a job. After Start, it starts at once. A job whose name is taken, or that is not
// valid, is an error.
func (s *Scheduler) Add(j Job) error {
	switch {
	case !jobName.MatchString(j.Name):
		return fmt.Errorf("jobs: %q is not a job name (lower-case words separated by single dashes or dots)", j.Name)
	case j.Every <= 0 || j.Timeout <= 0:
		return fmt.Errorf("jobs: %s needs a positive interval and timeout", j.Name)
	case j.Run == nil:
		return fmt.Errorf("jobs: %s has no Run", j.Name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return fmt.Errorf("jobs: %s added after the scheduler stopped", j.Name)
	}
	if _, ok := s.jobs[j.Name]; ok {
		return fmt.Errorf("jobs: %s is already scheduled", j.Name)
	}
	ctx, cancel := context.WithCancel(s.ctx)
	e := &entry{job: j, trigger: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}), ctx: ctx, cancel: cancel}
	s.jobs[j.Name] = e
	if s.started {
		s.launch(e)
	}
	return nil
}

func (s *Scheduler) launch(e *entry) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(e.done)
		s.loop(e)
	}()
}

// Remove stops a job: it is not run again, and a run in progress has its context canceled.
// Remove waits for that run to end until ctx ends. Removing a job that is not scheduled does
// nothing.
func (s *Scheduler) Remove(ctx context.Context, name string) error {
	s.mu.Lock()
	e, ok := s.jobs[name]
	delete(s.jobs, name)
	started := s.started
	s.mu.Unlock()
	if !ok {
		return nil
	}
	close(e.stop)
	e.cancel()
	if !started {
		return nil
	}
	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("jobs: %s did not stop: %w", name, ctx.Err())
	}
}

// Trigger asks for a run of the job as soon as possible, even if it is not due; it reports
// whether the job is scheduled. A trigger while the job runs causes one more run after it.
// Another instance may still hold the lease, in which case the run waits for it.
func (s *Scheduler) Trigger(name string) bool {
	s.mu.Lock()
	e, ok := s.jobs[name]
	s.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case e.trigger <- struct{}{}:
	default:
	}
	return true
}

// Start runs the jobs added so far, and those added later. Starting twice does nothing.
func (s *Scheduler) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.stopped {
		return
	}
	s.started = true
	for _, name := range slices.Sorted(maps.Keys(s.jobs)) {
		s.launch(s.jobs[name])
	}
}

// Stop cancels every run in progress and waits until the jobs have stopped, or ctx ends.
func (s *Scheduler) Stop(ctx context.Context) error {
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
	s.cancel()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("jobs: still running at the end of the shutdown: %w", ctx.Err())
	}
}

func (s *Scheduler) loop(e *entry) {
	var wait time.Duration // try at once: the job may be due since before the restart
	force := false
	for {
		timer := time.NewTimer(wait)
		select {
		case <-e.stop:
			timer.Stop()
			return
		case <-e.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-e.trigger:
			timer.Stop()
			force = true
		}
		wait, force = s.attempt(e, force)
	}
}

// state is a job's row as another instance left it.
type state struct {
	nextRun    sql.NullInt64
	leaseUntil sql.NullInt64
}

// attempt takes the lease and runs the job when it is due (or forced). It returns how long to
// wait before the next attempt, and whether a forced run is still owed.
func (s *Scheduler) attempt(e *entry, force bool) (time.Duration, bool) {
	j := e.job
	now := time.Now()
	got, st, err := s.acquire(e.ctx, j, now, force)
	if err != nil {
		if e.ctx.Err() == nil {
			s.log.Error("job: cannot take the lease", "job", j.Name, "err", err)
		}
		return min(s.retry, j.Every), force
	}
	if !got {
		until := now.Add(j.Every)
		if st.leaseUntil.Valid && dbx.Time(st.leaseUntil.Int64).After(now) {
			// Another instance is running it. A forced run waits for it to finish.
			until = dbx.Time(st.leaseUntil.Int64)
		} else if st.nextRun.Valid {
			until = dbx.Time(st.nextRun.Int64)
		}
		return clamp(until.Sub(now), j.Every), force && st.leaseUntil.Valid
	}

	result, runErr, returned := s.run(e)
	took := time.Since(now)
	next := now.Add(j.Every)
	if end := time.Now(); next.Before(end) {
		next = end
	}
	s.record(j, now, result, runErr, next, returned)
	if s.rec != nil {
		s.rec.JobFinished(j.Name, result, took)
	}
	return clamp(time.Until(next), j.Every), false
}

func clamp(d, most time.Duration) time.Duration {
	return max(min(d, most), 10*time.Millisecond)
}

// acquire takes the job's lease when nobody holds it and the job is due (or force is set).
func (s *Scheduler) acquire(ctx context.Context, j Job, now time.Time, force bool) (bool, state, error) {
	var got bool
	var st state
	err := s.db.InTx(ctx, func(tx *dbx.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO jobs (name) VALUES (?) ON CONFLICT (name) DO NOTHING`, j.Name); err != nil {
			return err
		}
		q := `UPDATE jobs SET lease_owner = ?, lease_until = ?
		       WHERE name = ? AND (lease_until IS NULL OR lease_until <= ?)`
		args := []any{s.owner, dbx.Millis(now.Add(j.Timeout + s.grace)), j.Name, dbx.Millis(now)}
		if !force {
			q += ` AND (next_run_at IS NULL OR next_run_at <= ?)`
			args = append(args, dbx.Millis(now))
		}
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if got = n == 1; got {
			return nil
		}
		return tx.QueryRowContext(ctx, `SELECT next_run_at, lease_until FROM jobs WHERE name = ?`, j.Name).
			Scan(&st.nextRun, &st.leaseUntil)
	})
	return got, st, err
}

// run runs the job once under its timeout. returned is false when the job ignored its canceled
// context for longer than the grace period; it is then still running.
func (s *Scheduler) run(e *entry) (result Result, err error, returned bool) {
	ctx, cancel := context.WithTimeout(e.ctx, e.job.Timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- fmt.Errorf("panic: %v\n%s", p, debug.Stack())
			}
		}()
		done <- e.job.Run(ctx)
	}()
	returned = true
	select {
	case err = <-done:
	case <-ctx.Done():
		select {
		case err = <-done:
		case <-time.After(s.grace):
			returned = false
			err = ctx.Err()
			s.log.Error("job: still running after its context was canceled; it is left running", "job", e.job.Name, "grace", s.grace.String())
		}
	}
	switch {
	case err == nil:
		return OK, nil, returned
	case e.ctx.Err() != nil:
		return Canceled, err, returned
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return TimedOut, err, returned
	}
	return Failed, err, returned
}

// maxErrorLen bounds jobs.last_error.
const maxErrorLen = 1000

// record writes the outcome of a run and releases the lease, unless the run is still going.
func (s *Scheduler) record(j Job, started time.Time, result Result, runErr error, next time.Time, returned bool) {
	msg := ""
	if runErr != nil {
		msg = clip(logging.Redact(runErr.Error()), maxErrorLen)
	}
	switch result {
	case OK:
		s.log.Debug("job finished", "job", j.Name, "took", time.Since(started).String())
	case Canceled:
		s.log.Warn("job canceled", "job", j.Name, "err", msg)
	default:
		s.log.Error("job "+string(result), "job", j.Name, "err", msg)
	}
	q := `UPDATE jobs SET last_run_at = ?, last_result = ?, last_error = ?, next_run_at = ?, lease_owner = '', lease_until = NULL
	       WHERE name = ? AND lease_owner = ?`
	if !returned {
		// Keep the lease until it expires: the run is still going.
		q = `UPDATE jobs SET last_run_at = ?, last_result = ?, last_error = ?, next_run_at = ? WHERE name = ? AND lease_owner = ?`
	}
	// Record even while shutting down.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 10*time.Second)
	defer cancel()
	res, err := s.db.ExecContext(ctx, q, dbx.Millis(started), string(result), msg, dbx.Millis(next), j.Name, s.owner)
	if err != nil {
		s.log.Error("job: cannot record the result", "job", j.Name, "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		s.log.Warn("job: the lease expired during the run; another instance may have run it too", "job", j.Name)
	}
}

// clip cuts s to at most n bytes, at a character boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// Status is a job's row in the jobs table.
type Status struct {
	Name       string
	LastRunAt  time.Time // zero when it never ran
	LastResult Result    // empty when it never ran
	LastError  string
	NextRunAt  time.Time // zero when not known yet
	LeaseOwner string    // the instance running it, if any
	LeaseUntil time.Time
}

// Statuses returns the row of every job that ever ran or was scheduled on this database, by
// name.
func (s *Scheduler) Statuses(ctx context.Context) ([]Status, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, last_run_at, last_result, last_error, next_run_at, lease_owner, lease_until FROM jobs ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Status
	for rows.Next() {
		var st Status
		var last, next, until sql.NullInt64
		var result string
		if err := rows.Scan(&st.Name, &last, &result, &st.LastError, &next, &st.LeaseOwner, &until); err != nil {
			return nil, err
		}
		st.LastResult = Result(result)
		if last.Valid {
			st.LastRunAt = dbx.Time(last.Int64)
		}
		if next.Valid {
			st.NextRunAt = dbx.Time(next.Int64)
		}
		if until.Valid {
			st.LeaseUntil = dbx.Time(until.Int64)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

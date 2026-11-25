// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package jobs_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
	"github.com/code2rich/rigdepot/internal/server/jobs"
	"github.com/code2rich/rigdepot/internal/server/migrations"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// eachDB runs fn on every dialect with the URL of a fresh, migrated database, so that a test can
// open it more than once (one handle per simulated instance).
func eachDB(t *testing.T, fn func(t *testing.T, url string)) {
	for _, target := range []struct {
		name string
		url  func(testing.TB) string
	}{{dbx.SQLite.String(), dbxtest.SQLiteURL}, {dbx.Postgres.String(), dbxtest.PostgresURL}} {
		t.Run(target.name, func(t *testing.T) {
			url := target.url(t)
			if _, err := migrations.Apply(context.Background(), dbxtest.Open(t, url)); err != nil {
				t.Fatal(err)
			}
			fn(t, url)
		})
	}
}

func newScheduler(t *testing.T, db *dbx.DB, rec jobs.Recorder) *jobs.Scheduler {
	t.Helper()
	s := jobs.New(db, jobs.Options{Metrics: rec, Grace: 200 * time.Millisecond, RetryAfter: 50 * time.Millisecond})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	return s
}

// waitStatus polls the jobs table until ok accepts the job's row.
func waitStatus(t *testing.T, s *jobs.Scheduler, name string, ok func(jobs.Status) bool) jobs.Status {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var last jobs.Status
	for time.Now().Before(deadline) {
		all, err := s.Statuses(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, st := range all {
			if st.Name == name {
				last = st
				if ok(st) {
					return st
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s never reached the expected state; last row: %+v", name, last)
	return last
}

// TestTimeoutCancelsTheRun: a run that reaches its timeout has its context canceled, and the
// outcome is "timeout" in the table and in the metrics.
func TestTimeoutCancelsTheRun(t *testing.T) {
	eachDB(t, func(t *testing.T, url string) {
		counters := jobs.NewCounters()
		s := newScheduler(t, dbxtest.Open(t, url), counters)
		saw := make(chan error, 1)
		err := s.Add(jobs.Job{Name: "stuck", Every: time.Hour, Timeout: 50 * time.Millisecond, Run: func(ctx context.Context) error {
			<-ctx.Done()
			saw <- ctx.Err()
			return ctx.Err()
		}})
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		s.Start()
		select {
		case err := <-saw:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("the run's context ended with %v, want its deadline", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the run was never canceled")
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("the 50ms timeout took %v to cancel the run", d)
		}
		st := waitStatus(t, s, "stuck", func(st jobs.Status) bool { return st.LastResult != "" })
		if st.LastResult != jobs.TimedOut || st.LastError == "" || st.LeaseOwner != "" {
			t.Errorf("row after the timeout: %+v; want result timeout, an error and the lease released", st)
		}
		if n := counters.Runs("stuck", jobs.TimedOut); n != 1 {
			t.Errorf("metrics count %d timed-out runs, want 1: %+v", n, counters.Snapshot())
		}
	})
}

// TestOneRunAcrossInstances: two instances on one database run a job once per interval, never
// at the same time, also when both are asked to run it right now.
func TestOneRunAcrossInstances(t *testing.T) {
	eachDB(t, func(t *testing.T, url string) {
		var runs, running, most atomic.Int32
		job := jobs.Job{Name: "source-sync", Every: time.Hour, Timeout: 10 * time.Second, Run: func(ctx context.Context) error {
			n := running.Add(1)
			defer running.Add(-1)
			for {
				m := most.Load()
				if n <= m || most.CompareAndSwap(m, n) {
					break
				}
			}
			runs.Add(1)
			time.Sleep(100 * time.Millisecond)
			return nil
		}}
		a := newScheduler(t, dbxtest.Open(t, url), nil)
		b := newScheduler(t, dbxtest.Open(t, url), nil)
		for _, s := range []*jobs.Scheduler{a, b} {
			if err := s.Add(job); err != nil {
				t.Fatal(err)
			}
		}
		a.Start()
		b.Start()
		waitStatus(t, a, job.Name, func(st jobs.Status) bool { return st.LastResult == jobs.OK })
		time.Sleep(300 * time.Millisecond) // time for a second, wrong run to show
		if n := runs.Load(); n != 1 {
			t.Fatalf("both instances started: %d runs in one interval, want 1", n)
		}

		// Both asked to run it now, several times over.
		for range 5 {
			var wg sync.WaitGroup
			for _, s := range []*jobs.Scheduler{a, b} {
				wg.Go(func() { s.Trigger(job.Name) })
			}
			wg.Wait()
			time.Sleep(30 * time.Millisecond)
		}
		deadline := time.Now().Add(10 * time.Second)
		for runs.Load() < 3 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(300 * time.Millisecond)
		if m := most.Load(); m != 1 {
			t.Errorf("%d runs at the same time, want 1", m)
		}
		if n := runs.Load(); n < 3 {
			t.Errorf("triggers ran the job %d times in all, want a few", n)
		}
	})
}

// TestResultsAreRecorded: success, an error and a panic each land in the table and the metrics;
// a password in the error does not.
func TestResultsAreRecorded(t *testing.T) {
	eachDB(t, func(t *testing.T, url string) {
		counters := jobs.NewCounters()
		s := newScheduler(t, dbxtest.Open(t, url), counters)
		add := func(name string, run func(context.Context) error) {
			t.Helper()
			if err := s.Add(jobs.Job{Name: name, Every: time.Hour, Timeout: 5 * time.Second, Run: run}); err != nil {
				t.Fatal(err)
			}
		}
		add("works", func(context.Context) error { return nil })
		add("fails", func(context.Context) error {
			return errors.New("connect postgres://app:hunter2-secret@db.example.com/rgd: refused")
		})
		add("panics", func(context.Context) error { panic("boom") })
		before := time.Now().Add(-time.Second)
		s.Start()

		ok := waitStatus(t, s, "works", func(st jobs.Status) bool { return st.LastResult != "" })
		if ok.LastResult != jobs.OK || ok.LastError != "" || ok.LastRunAt.Before(before) || !ok.NextRunAt.After(time.Now().Add(50*time.Minute)) {
			t.Errorf("row of a successful run: %+v", ok)
		}
		failed := waitStatus(t, s, "fails", func(st jobs.Status) bool { return st.LastResult != "" })
		if failed.LastResult != jobs.Failed || !strings.Contains(failed.LastError, "refused") {
			t.Errorf("row of a failed run: %+v", failed)
		}
		if strings.Contains(failed.LastError, "hunter2") {
			t.Errorf("the password reached the jobs table: %q", failed.LastError)
		}
		panicked := waitStatus(t, s, "panics", func(st jobs.Status) bool { return st.LastResult != "" })
		if panicked.LastResult != jobs.Failed || !strings.Contains(panicked.LastError, "boom") {
			t.Errorf("row of a run that panicked: %+v", panicked)
		}
		for name, want := range map[string]jobs.Result{"works": jobs.OK, "fails": jobs.Failed, "panics": jobs.Failed} {
			if n := counters.Runs(name, want); n != 1 {
				t.Errorf("metrics: %s has %d runs with result %s, want 1", name, n, want)
			}
		}
	})
}

// TestRestartKeepsTheSchedule: a new instance does not run a job again before it is due, and a
// trigger runs it anyway.
func TestRestartKeepsTheSchedule(t *testing.T) {
	eachDB(t, func(t *testing.T, url string) {
		var runs atomic.Int32
		job := jobs.Job{Name: "expire", Every: time.Hour, Timeout: 5 * time.Second, Run: func(context.Context) error { runs.Add(1); return nil }}
		first := jobs.New(dbxtest.Open(t, url), jobs.Options{})
		if err := first.Add(job); err != nil {
			t.Fatal(err)
		}
		first.Start()
		waitStatus(t, first, job.Name, func(st jobs.Status) bool { return st.LastResult == jobs.OK })
		if err := first.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}

		second := newScheduler(t, dbxtest.Open(t, url), nil)
		if err := second.Add(job); err != nil {
			t.Fatal(err)
		}
		second.Start()
		time.Sleep(300 * time.Millisecond)
		if n := runs.Load(); n != 1 {
			t.Fatalf("the restarted instance ran the job before it was due: %d runs", n)
		}
		if !second.Trigger(job.Name) {
			t.Fatal("Trigger did not find the job")
		}
		deadline := time.Now().Add(10 * time.Second)
		for runs.Load() < 2 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if n := runs.Load(); n != 2 {
			t.Errorf("the trigger did not run the job: %d runs", n)
		}
	})
}

// TestRemoveCancelsTheRun: removing a job cancels a run in progress and records it as canceled.
func TestRemoveCancelsTheRun(t *testing.T) {
	eachDB(t, func(t *testing.T, url string) {
		s := newScheduler(t, dbxtest.Open(t, url), nil)
		started := make(chan struct{})
		err := s.Add(jobs.Job{Name: "long", Every: time.Hour, Timeout: time.Minute, Run: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}})
		if err != nil {
			t.Fatal(err)
		}
		s.Start()
		<-started
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Remove(ctx, "long"); err != nil {
			t.Fatal(err)
		}
		st := waitStatus(t, s, "long", func(st jobs.Status) bool { return st.LastResult != "" })
		if st.LastResult != jobs.Canceled || st.LeaseOwner != "" {
			t.Errorf("row after Remove: %+v; want canceled and the lease released", st)
		}
		if s.Trigger("long") {
			t.Error("a removed job can still be triggered")
		}
	})
}

func TestAddRejects(t *testing.T) {
	s := jobs.New(nil, jobs.Options{})
	run := func(context.Context) error { return nil }
	for name, j := range map[string]jobs.Job{
		"bad name":    {Name: "Mail_Outbox", Every: time.Minute, Timeout: time.Minute, Run: run},
		"no interval": {Name: "a", Timeout: time.Minute, Run: run},
		"no timeout":  {Name: "a", Every: time.Minute, Run: run},
		"no run":      {Name: "a", Every: time.Minute, Timeout: time.Minute},
	} {
		if err := s.Add(j); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := s.Add(jobs.Job{Name: "mail-outbox", Every: time.Minute, Timeout: time.Minute, Run: run}); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(jobs.Job{Name: "mail-outbox", Every: time.Minute, Timeout: time.Minute, Run: run}); err == nil {
		t.Error("the same name was accepted twice")
	}
}

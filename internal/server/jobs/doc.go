// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package jobs schedules background jobs with timeouts and leases.
//
// A job has a name, an interval and a timeout. The scheduler runs it every interval, cancels its
// context when the timeout is reached, and writes the outcome of every run to the jobs table
// (last run, result, error, next run) and to a Recorder for metrics. Trigger runs a job right
// away, for "fetch now" buttons and for work that has just been queued.
//
// # One run at a time, across instances
//
// Several instances of the server can share one database. Before running a job an instance
// takes the job's lease in the jobs table with one conditional UPDATE: it succeeds only when no
// other instance holds an unexpired lease and the job is due. The lease lasts for the timeout
// plus a grace period, so an instance that dies mid-run blocks the job for that long at most.
// The next run time is in the table too, so two instances do not each run a job once per
// interval: whichever takes the lease first runs it, and the other one finds it not due.
//
// Leases compare the clocks of the instances. Keep them synchronized (NTP); a clock that is off
// by more than the grace period can let a run overlap the next.
//
// A single SQLite deployment goes through the same code.
package jobs

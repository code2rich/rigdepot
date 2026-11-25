// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package jobs

import (
	"cmp"
	"slices"
	"sync"
	"time"
)

// Counters is a Recorder that keeps counts in memory: runs and total run time per job and
// result. The metrics endpoint reads it with Snapshot.
type Counters struct {
	mu     sync.Mutex
	counts map[countKey]*Count
}

type countKey struct {
	job    string
	result Result
}

// Count is the tally of one job and result.
type Count struct {
	Job     string
	Result  Result
	Runs    int64
	Seconds float64 // total run time
}

// NewCounters returns empty counters.
func NewCounters() *Counters { return &Counters{counts: map[countKey]*Count{}} }

// JobFinished implements Recorder.
func (c *Counters) JobFinished(name string, result Result, took time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := countKey{name, result}
	n, ok := c.counts[k]
	if !ok {
		n = &Count{Job: name, Result: result}
		c.counts[k] = n
	}
	n.Runs++
	n.Seconds += took.Seconds()
}

// Runs returns how many runs of the job ended with the result.
func (c *Counters) Runs(job string, result Result) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n, ok := c.counts[countKey{job, result}]; ok {
		return n.Runs
	}
	return 0
}

// Snapshot returns every count, by job and result.
func (c *Counters) Snapshot() []Count {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Count, 0, len(c.counts))
	for _, n := range c.counts {
		out = append(out, *n)
	}
	slices.SortFunc(out, func(a, b Count) int {
		return cmp.Or(cmp.Compare(a.Job, b.Job), cmp.Compare(a.Result, b.Result))
	})
	return out
}

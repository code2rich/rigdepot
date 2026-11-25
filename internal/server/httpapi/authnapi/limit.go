// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package authnapi

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// Rate is a token bucket: up to Burst requests at once, then one more every
// Every.
type Rate struct {
	Burst int
	Every time.Duration
}

// The default limits (docs/architecture/06-server.md#安全基线): signing in,
// invitation links and the setup token by client address; everything else
// by account.
var (
	DefaultSignInRate = Rate{Burst: 10, Every: 6 * time.Second}        // 10 a minute after the first 10
	DefaultUserRate   = Rate{Burst: 300, Every: 50 * time.Millisecond} // 20 a second after the first 300
)

// limiter keeps one bucket per key, in memory only: a restart forgets them,
// and each instance counts its own (like the feedback service).
type limiter struct {
	rate Rate
	now  func() time.Time

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newLimiter(r Rate, now func() time.Time) *limiter {
	return &limiter{rate: r, now: now, buckets: map[string]*bucket{}}
}

// allow takes one token from key's bucket. When there is none it returns
// false and how long until there is one.
func (l *limiter) allow(key string) (time.Duration, bool) {
	if l == nil || l.rate.Burst <= 0 || l.rate.Every <= 0 {
		return 0, true
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(l.rate.Burst), at: now}
		l.buckets[key] = b
	}
	b.tokens = min(float64(l.rate.Burst), b.tokens+float64(now.Sub(b.at))/float64(l.rate.Every))
	b.at = now
	if b.tokens < 1 {
		return time.Duration((1 - b.tokens) * float64(l.rate.Every)), false
	}
	b.tokens--
	return 0, true
}

// sweep forgets the buckets that have filled up again, once a minute, so
// that the map does not grow with every address ever seen.
func (l *limiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < time.Minute {
		return
	}
	l.lastSweep = now
	full := time.Duration(l.rate.Burst) * l.rate.Every
	for k, b := range l.buckets {
		if now.Sub(b.at) >= full {
			delete(l.buckets, k)
		}
	}
}

// clientAddr returns the address a request comes from: the peer, or, when
// the peer is a trusted reverse proxy, the last address in X-Forwarded-For
// that is not one of the trusted proxies. Only trusted proxies are believed:
// anyone else can write any X-Forwarded-For.
func clientAddr(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	addr = addr.Unmap()
	isTrusted := func(a netip.Addr) bool {
		for _, p := range trusted {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
	if !isTrusted(addr) {
		return addr.String()
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		addr = hop.Unmap()
		if !isTrusted(addr) {
			break
		}
	}
	return addr.String()
}

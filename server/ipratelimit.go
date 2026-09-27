package main

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// keyedRateLimiter is a token bucket keyed by an arbitrary string. It
// complements the global limiters in ratelimit.go: those cap our total
// outbound rate to each upstream API across every client combined; this caps
// how much of that shared budget a single key can burn through. A script
// hammering hundreds of never-before-seen hotspots gets throttled here long
// before it can monopolize the global budget (and starve everyone else) or
// force a meaningful amount of fresh upstream traffic.
//
// Keys are authenticated account ids for the hotspot endpoints (see
// accountKey and auth.go) and source IPs for the pre-login OAuth endpoints,
// where no account exists yet.
//
// Deliberately not one goroutine-per-key like rateLimiter in ratelimit.go —
// with potentially thousands of distinct keys that doesn't scale. Each bucket
// instead refills lazily based on elapsed time on access, and idle buckets
// are swept periodically so memory stays bounded no matter how many distinct
// keys show up.
type keyedRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*rateBucket
	rate    float64 // tokens/sec
	burst   float64
}

type rateBucket struct {
	tokens   float64
	lastSeen time.Time
}

const rateBucketIdleTTL = 30 * time.Minute

func newKeyedRateLimiter(burst int, perMinute float64) *keyedRateLimiter {
	l := &keyedRateLimiter{
		buckets: make(map[string]*rateBucket),
		rate:    perMinute / 60,
		burst:   float64(burst),
	}
	go l.sweepLoop()
	return l
}

// newIPRateLimiter is a keyedRateLimiter keyed by client IP, used for the
// login/callback endpoints that run before an account is known.
func newIPRateLimiter(burst int, perMinute float64) *keyedRateLimiter {
	return newKeyedRateLimiter(burst, perMinute)
}

func (l *keyedRateLimiter) sweepLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-rateBucketIdleTTL)
		l.mu.Lock()
		for key, b := range l.buckets {
			if b.lastSeen.Before(cutoff) {
				delete(l.buckets, key)
			}
		}
		l.mu.Unlock()
	}
}

// allow reports whether key may make one more request right now, consuming a
// token if so.
func (l *keyedRateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b, ok := l.buckets[key]
	if !ok {
		b = &rateBucket{tokens: l.burst - 1, lastSeen: now}
		l.buckets[key] = b
		return true
	}
	elapsed := now.Sub(b.lastSeen).Seconds()
	b.tokens = min(l.burst, b.tokens+elapsed*l.rate)
	b.lastSeen = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// middleware rejects requests over the limit for the key extracted from the
// request by keyFn.
func (l *keyedRateLimiter) middleware(keyFn func(*http.Request) string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(keyFn(r)) {
			w.Header().Set("Retry-After", "6")
			http.Error(w, "too many requests, slow down", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

// accountKey is the keyFn for authenticated endpoints: the account's stable
// id, attached to the request context by Auth.require. Callers must have run
// the auth middleware first; an empty key is still rate-limited, just
// collectively, which is a safe fallback rather than an open door.
func accountKey(r *http.Request) string {
	return userIDFromContext(r)
}

// clientIP returns the request's source IP. Only RemoteAddr is trusted —
// X-Forwarded-For is attacker-controlled unless a reverse proxy is known to
// set it, which this deployment doesn't assume.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

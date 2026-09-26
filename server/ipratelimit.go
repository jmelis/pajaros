package main

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// ipRateLimiter is a per-client-IP token bucket. It complements the global
// limiters in ratelimit.go: those cap our total outbound rate to each
// upstream API across every client combined; this caps how much of that
// shared budget a single IP can burn through. A script hammering hundreds of
// never-before-seen hotspots gets throttled here long before it can
// monopolize the global budget (and starve everyone else) or force a
// meaningful amount of fresh upstream traffic.
//
// Deliberately not one goroutine-per-IP like rateLimiter in ratelimit.go —
// with potentially thousands of distinct IPs that doesn't scale. Each bucket
// instead refills lazily based on elapsed time on access, and idle buckets
// are swept periodically so memory stays bounded no matter how many distinct
// IPs show up.
type ipRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*ipBucket
	rate    float64 // tokens/sec
	burst   float64
}

type ipBucket struct {
	tokens   float64
	lastSeen time.Time
}

const ipBucketIdleTTL = 30 * time.Minute

func newIPRateLimiter(burst int, perMinute float64) *ipRateLimiter {
	l := &ipRateLimiter{
		buckets: make(map[string]*ipBucket),
		rate:    perMinute / 60,
		burst:   float64(burst),
	}
	go l.sweepLoop()
	return l
}

func (l *ipRateLimiter) sweepLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-ipBucketIdleTTL)
		l.mu.Lock()
		for ip, b := range l.buckets {
			if b.lastSeen.Before(cutoff) {
				delete(l.buckets, ip)
			}
		}
		l.mu.Unlock()
	}
}

// allow reports whether ip may make one more request right now, consuming a
// token if so.
func (l *ipRateLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b, ok := l.buckets[ip]
	if !ok {
		b = &ipBucket{tokens: l.burst - 1, lastSeen: now}
		l.buckets[ip] = b
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

// middleware rejects requests over ip's limit with 429 before next runs.
func (l *ipRateLimiter) middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(clientIP(r)) {
			w.Header().Set("Retry-After", "6")
			http.Error(w, "too many requests, slow down", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
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

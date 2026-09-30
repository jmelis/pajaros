package main

import (
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"time"
)

// birdquizUA identifies this project (and gives a contact point) to
// Wikimedia — the only upstream API the deployed server still talks to; eBird and GBIF are both build-time-only now (see
// ARCHITECTURE.md and cmd/gensnapshot).
const birdquizUA = "birdquiz/0.1 (+https://github.com/jmelis/pajaros; contact: j.melis@gmail.com)"

// wikimediaLimiter caps the *aggregate* outbound rate to Wikipedia/Commons
// across all goroutines and all species — the per-species image-fetch
// concurrency (imageFetchConcurrency) controls pipelining, this controls how
// hard we actually hit their servers.
//
// This same knob governs both live per-hotspot lookups (want it fast — a
// new hotspot's ~20 species means ~60 requests) and any future bulk
// cache-warming job across many hotspots (want it slow and polite). Default
// favors live traffic; set WIKIMEDIA_RPS low (e.g. 1) for a deliberate,
// hours-long bulk run instead.
var wikimediaLimiter = newRateLimiter("wikimedia", rpsFromEnv("WIKIMEDIA_RPS", 8))

func rpsFromEnv(envVar string, def float64) float64 {
	if v := os.Getenv(envVar); v != "" {
		if rps, err := strconv.ParseFloat(v, 64); err == nil && rps > 0 {
			return rps
		}
	}
	return def
}

type rateLimiter struct {
	name   string // metrics label — see upstreamRequestsTotal/upstreamRetriesTotal
	tokens chan struct{}
}

func newRateLimiter(name string, perSecond float64) *rateLimiter {
	rl := &rateLimiter{name: name, tokens: make(chan struct{}, 1)}
	go func() {
		ticker := time.NewTicker(time.Duration(float64(time.Second) / perSecond))
		defer ticker.Stop()
		for range ticker.C {
			select {
			case rl.tokens <- struct{}{}:
			default: // bucket already full, drop the tick rather than block
			}
		}
	}()
	return rl
}

func (rl *rateLimiter) wait() {
	<-rl.tokens
}

const maxRetries = 4

// doThrottled runs req through the given rate limiter and retries on 429s
// (honoring Retry-After) and transient 5xx/network errors, with exponential
// backoff + jitter. Callers must close the returned response body.
func doThrottled(limiter *rateLimiter, req *http.Request) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			upstreamRetriesTotal.WithLabelValues(limiter.name).Inc()
		}
		limiter.wait()

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = err
			upstreamRequestsTotal.WithLabelValues(limiter.name, statusClass(0)).Inc()
			sleepBackoff(attempt, 0)
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
			resp.Body.Close()
			lastErr = errStatus(resp.StatusCode)
			upstreamRequestsTotal.WithLabelValues(limiter.name, statusClass(resp.StatusCode)).Inc()
			sleepBackoff(attempt, retryAfter)
			continue
		}
		if resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = errStatus(resp.StatusCode)
			upstreamRequestsTotal.WithLabelValues(limiter.name, statusClass(resp.StatusCode)).Inc()
			sleepBackoff(attempt, 0)
			continue
		}
		upstreamRequestsTotal.WithLabelValues(limiter.name, statusClass(resp.StatusCode)).Inc()
		return resp, nil
	}
	return nil, lastErr
}

func sleepBackoff(attempt int, minWait time.Duration) {
	backoff := time.Duration(1<<attempt) * time.Second
	jitter := time.Duration(rand.Int63n(int64(500 * time.Millisecond)))
	wait := backoff + jitter
	if minWait > wait {
		wait = minWait
	}
	time.Sleep(wait)
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second
	}
	return 0
}

type httpStatusError int

func (e httpStatusError) Error() string {
	return "unexpected status " + strconv.Itoa(int(e))
}

func errStatus(code int) error {
	return httpStatusError(code)
}

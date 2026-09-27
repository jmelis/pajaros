package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestKeyedRateLimiterPerKey(t *testing.T) {
	// rate 0 means no refill, so the bucket is a simple burst counter.
	l := newKeyedRateLimiter(2, 0)

	if !l.allow("acct-a") || !l.allow("acct-a") {
		t.Fatal("first two requests for acct-a should be allowed")
	}
	if l.allow("acct-a") {
		t.Fatal("third request for acct-a should be denied")
	}
	// A different key must have its own untouched bucket.
	if !l.allow("acct-b") {
		t.Fatal("acct-b should not be affected by acct-a's usage")
	}
}

func TestKeyedRateLimiterMiddlewareUsesAccountKey(t *testing.T) {
	l := newKeyedRateLimiter(1, 0)
	next := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
	handler := l.middleware(accountKey, next)

	do := func(account string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/hotspots", nil)
		ctx := context.WithValue(req.Context(), userContextKey{}, account)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req.WithContext(ctx))
		return rr.Code
	}

	if code := do("acct-a"); code != http.StatusOK {
		t.Fatalf("first request for acct-a = %d, want 200", code)
	}
	if code := do("acct-a"); code != http.StatusTooManyRequests {
		t.Fatalf("second request for acct-a = %d, want 429", code)
	}
	if code := do("acct-b"); code != http.StatusOK {
		t.Fatalf("first request for acct-b = %d, want 200", code)
	}
}

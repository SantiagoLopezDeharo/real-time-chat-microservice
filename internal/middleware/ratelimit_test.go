package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func requestAs(userID string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/messages", nil)
	if userID == "" {
		return req
	}
	return req.WithContext(context.WithValue(req.Context(), userIDKey{}, userID))
}

func countingHandler(rl *RateLimiter) (http.Handler, *atomic.Int64) {
	var passed atomic.Int64
	return rl.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { passed.Add(1) })), &passed
}

func TestRateLimiterAllowsBurstThenRejects(t *testing.T) {
	handler, passed := countingHandler(NewRateLimiter(rate.Limit(0.0001), 3))
	statuses := make([]int, 5)
	for i := range statuses {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, requestAs("alice"))
		statuses[i] = rec.Code
	}
	want := []int{200, 200, 200, 429, 429}
	for i := range want {
		if statuses[i] != want[i] {
			t.Fatalf("statuses = %v, want %v", statuses, want)
		}
	}
	if passed.Load() != 3 {
		t.Errorf("passed = %d", passed.Load())
	}
}

func TestRateLimiterIsPerUser(t *testing.T) {
	handler, passed := countingHandler(NewRateLimiter(rate.Limit(0.0001), 1))
	for _, user := range []string{"alice", "bob", "carol", "alice", "bob"} {
		handler.ServeHTTP(httptest.NewRecorder(), requestAs(user))
	}
	if passed.Load() != 3 {
		t.Errorf("passed = %d, want one per distinct user", passed.Load())
	}
}

func TestRateLimiterRequiresUser(t *testing.T) {
	handler, passed := countingHandler(NewRateLimiter(rate.Inf, 1))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, requestAs(""))
	if rec.Code != http.StatusUnauthorized || passed.Load() != 0 {
		t.Errorf("status = %d passed = %d", rec.Code, passed.Load())
	}
}

func TestRateLimiterConcurrentBurstIsExact(t *testing.T) {
	const burst = 50
	handler, passed := countingHandler(NewRateLimiter(rate.Limit(0.0001), burst))
	var wg sync.WaitGroup
	for range 400 {
		wg.Go(func() { handler.ServeHTTP(httptest.NewRecorder(), requestAs("alice")) })
	}
	wg.Wait()
	if passed.Load() != burst {
		t.Errorf("passed = %d, want exactly %d under concurrency", passed.Load(), burst)
	}
}

func TestRateLimiterEvictsIdleFullBuckets(t *testing.T) {
	rl := NewRateLimiter(rate.Limit(0.0001), 3)
	clock := time.Now()
	rl.now = func() time.Time { return clock }
	handler, passed := countingHandler(rl)
	size := func() int {
		rl.mu.Lock()
		defer rl.mu.Unlock()
		return len(rl.visitors)
	}

	for _, user := range []string{"alice", "bob", "carol"} {
		handler.ServeHTTP(httptest.NewRecorder(), requestAs(user))
	}

	clock = clock.Add(2 * evictionInterval)
	handler.ServeHTTP(httptest.NewRecorder(), requestAs("dave"))
	if got := size(); got != 4 {
		t.Fatalf("buckets that are not full again were evicted: %d tracked, want 4", got)
	}

	clock = clock.Add(24 * time.Hour)
	handler.ServeHTTP(httptest.NewRecorder(), requestAs("erin"))
	if got := size(); got != 1 {
		t.Fatalf("idle full buckets were kept: %d tracked, want 1", got)
	}

	before := passed.Load()
	for range 5 {
		handler.ServeHTTP(httptest.NewRecorder(), requestAs("alice"))
	}
	if got := passed.Load() - before; got != 3 {
		t.Errorf("evicted user got %d requests through, want a fresh burst of 3", got)
	}
}

func TestRateLimiterKeepsExhaustedBucketsAcrossSweeps(t *testing.T) {
	rl := NewRateLimiter(rate.Limit(0.0001), 2)
	clock := time.Now()
	rl.now = func() time.Time { return clock }
	handler, _ := countingHandler(rl)
	for range 2 {
		handler.ServeHTTP(httptest.NewRecorder(), requestAs("alice"))
	}
	for range 5 {
		clock = clock.Add(evictionInterval)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, requestAs("alice"))
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("sweep reset an exhausted bucket: status %d", rec.Code)
		}
	}
}

func BenchmarkRateLimiter(b *testing.B) {
	handler, _ := countingHandler(NewRateLimiter(rate.Inf, 1))
	req := requestAs("alice")
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			handler.ServeHTTP(httptest.NewRecorder(), req)
		}
	})
}

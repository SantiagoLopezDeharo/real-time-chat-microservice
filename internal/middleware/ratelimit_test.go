package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

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

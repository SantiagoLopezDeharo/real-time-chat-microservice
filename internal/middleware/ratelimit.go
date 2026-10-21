package middleware

import (
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const evictionInterval = time.Minute

type RateLimiter struct {
	visitors  map[string]*rate.Limiter
	mu        sync.Mutex
	rps       rate.Limit
	burst     int
	now       func() time.Time
	nextSweep time.Time
}

func NewRateLimiter(rps rate.Limit, burst int) *RateLimiter {
	return &RateLimiter{
		visitors: make(map[string]*rate.Limiter),
		rps:      rps,
		burst:    burst,
		now:      time.Now,
	}
}

// evictFull drops buckets that have refilled completely: a full bucket behaves
// exactly like a new one, so forgetting it cannot let a user exceed the limit.
func (rl *RateLimiter) evictFull(now time.Time) {
	for userID, limiter := range rl.visitors {
		if limiter.TokensAt(now) >= float64(rl.burst) {
			delete(rl.visitors, userID)
		}
	}
}

func (rl *RateLimiter) getVisitor(userID string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if now := rl.now(); !now.Before(rl.nextSweep) {
		rl.evictFull(now)
		rl.nextSweep = now.Add(evictionInterval)
	}

	limiter, exists := rl.visitors[userID]
	if !exists {
		limiter = rate.NewLimiter(rl.rps, rl.burst)
		rl.visitors[userID] = limiter
	}

	return limiter
}

func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := GetUserID(r)
		if !ok {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		limiter := rl.getVisitor(userID)
		if !limiter.Allow() {
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}

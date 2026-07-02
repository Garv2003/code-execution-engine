package middleware

import (
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

type clientEntry struct {
	count       atomic.Int64
	windowStart atomic.Int64
}

type RateLimiter struct {
	requestsPerMinute int
	clients           sync.Map
}

func NewRateLimiter(requestsPerMinute int) *RateLimiter {
	rl := &RateLimiter{
		requestsPerMinute: requestsPerMinute,
	}

	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now().Unix()
			rl.clients.Range(func(key, value any) bool {
				entry := value.(*clientEntry)
				if now-entry.windowStart.Load() >= 60 {
					rl.clients.Delete(key)
				}
				return true
			})
		}
	}()

	return rl
}

func (rl *RateLimiter) Limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rl.requestsPerMinute <= 0 {
			next.ServeHTTP(w, r)
			return
		}

		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}

		now := time.Now().Unix()
		val, _ := rl.clients.LoadOrStore(ip, &clientEntry{})
		entry := val.(*clientEntry)

		windowStart := entry.windowStart.Load()
		if now-windowStart >= 60 {
			entry.count.Store(1)
			entry.windowStart.Store(now)
			next.ServeHTTP(w, r)
			return
		}

		current := entry.count.Add(1)
		if current > int64(rl.requestsPerMinute) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]string{"error": "Rate limit exceeded"})
			return
		}

		next.ServeHTTP(w, r)
	})
}

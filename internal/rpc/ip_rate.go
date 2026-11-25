package rpc

import (
	"crypto/sha256"
	"github.com/invisi-labs/invisi-rpc/internal/iplimit"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type ipRateWindow struct {
	start time.Time
	count int
}

// LimitIPRequests bounds HTTP request frequency across chains. Batches count as
// one HTTP request, just as they do in the independent concurrency admission.
func LimitIPRequests(next http.Handler, cfg *iplimit.Config) (http.Handler, error) {
	return limitIPRequests(next, cfg, time.Now)
}
func limitIPRequests(next http.Handler, cfg *iplimit.Config, clock func() time.Time) (http.Handler, error) {
	policy, err := iplimit.New(cfg, 120)
	if err != nil {
		return nil, err
	}
	if policy.Limit() == 0 {
		return next, nil
	}
	var mu sync.Mutex
	clients := map[[32]byte]ipRateWindow{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if policy.Exempt(r) {
			next.ServeHTTP(w, r)
			return
		}
		now := clock()
		key := sha256.Sum256([]byte(policy.Key(r)))
		mu.Lock()
		item, exists := clients[key]
		if !exists && len(clients) >= 4096 {
			for k, v := range clients {
				if now.Sub(v.start) >= time.Minute {
					delete(clients, k)
				}
			}
		}
		full := !exists && len(clients) >= 4096
		if item.start.IsZero() || now.Sub(item.start) >= time.Minute {
			item = ipRateWindow{start: now}
		}
		allowed := !full && item.count < policy.Limit()
		if allowed {
			item.count++
			clients[key] = item
		}
		retry := max(1, int((time.Minute-now.Sub(item.start)+time.Second-1)/time.Second))
		mu.Unlock()
		if !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			WriteHTTPError(w, http.StatusTooManyRequests, "request rate reached")
			return
		}
		next.ServeHTTP(w, r)
	}), nil
}

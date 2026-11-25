package rpc

import "net/http"

// LimitConcurrentRequests bounds work across every chain and batch on one listener.
// Peer-IP frequency admission is separate and cannot bypass these slots.
func LimitConcurrentRequests(next http.Handler, maxConcurrent int) http.Handler {
	slots := make(chan struct{}, maxConcurrent)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"request capacity reached"}`))
		}
	})
}

package rpc

import "net/http"

// LimitConcurrentRequests bounds work across every chain and batch on one listener.
// Identity-based rates and authentication belong to the deployment's HTTP gateway.
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

type capacityError struct{}

func (capacityError) Error() string          { return "detection capacity reached; retry later" }
func (capacityError) ErrorCode() int         { return -32005 }
func (capacityError) ErrorData() interface{} { return map[string]int{"retryAfterSeconds": 1} }

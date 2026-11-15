package rpc

import (
	"encoding/json"
	"net/http"
)

// WriteHTTPError writes a static transport error without request or upstream data.
// Transport failures have no safely parsed request identifier.
func WriteHTTPError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      any    `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{JSONRPC: "2.0", Error: struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}{-32600, message}})
}

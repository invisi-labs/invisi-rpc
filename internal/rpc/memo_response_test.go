package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
	"strings"
	"testing"
)

func TestMemoUpdatesPreserveMaximumEnvelopes(t *testing.T) {
	backend, anchor := anchor()
	backend.outputs = make([]model.Output, 1000)
	for i := range backend.outputs {
		backend.outputs[i] = model.Output{Ciphertext: bytes.Repeat([]byte{4}, 658), Clue: bytes.Repeat([]byte{1}, 68)}
	}
	api, err := New(backend, model.Deployment{ChainID: "31337"}, 24, false, "1")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := HTTPHandler(api)
	if err != nil {
		t.Fatal(err)
	}
	response := call(t, handler, "invisi_getUpdates", []any{anchor}, "")
	if response.Error != nil {
		t.Fatal(response.Error.Message)
	}
	var updates Updates
	if err := json.Unmarshal(response.Result, &updates); err != nil {
		t.Fatal(err)
	}
	if len(updates.Outputs) != 1000 || !bytes.Equal(updates.Outputs[999].Ciphertext, backend.outputs[999].Ciphertext) {
		t.Fatal("maximum memo envelopes were truncated in the HTTP response")
	}
}

func TestMemoUpdatesRejectOversizedResponse(t *testing.T) {
	backend, anchor := anchor()
	backend.outputs = make([]model.Output, 9999)
	for i := range backend.outputs {
		backend.outputs[i] = model.Output{Ciphertext: bytes.Repeat([]byte{4}, 658), Clue: bytes.Repeat([]byte{1}, 68)}
	}
	api, err := New(backend, model.Deployment{ChainID: "31337"}, 24, false, "1")
	if err != nil {
		t.Fatal(err)
	}
	updates, err := api.GetUpdates(context.Background(), anchor)
	if err == nil || updates != nil || !strings.Contains(err.Error(), "smaller ranges") {
		t.Fatalf("expected explicit smaller-range error, got %v", err)
	}
}

package finality

import (
	"context"
	"errors"
	"testing"

	"github.com/invisi-labs/invisi-rpc/internal/model"
)

func TestValidatedPublicationRetriesChangedAnchorGeneration(t *testing.T) {
	ctx := context.Background()
	r, rpc, store := testReader()
	if err := r.Check(ctx); err != nil {
		t.Fatal(err)
	}
	durable := *store.block
	next := model.Block{Number: 8, Hash: rpc.headers["0x8"].Hash(), Timestamp: 8}
	r.RPC = &fenceDuringAnchorRPC{RPC: rpc, numericCalls: 1, beforeAnchor: func() { r.protectAnchor(next) }}
	release, err := r.AcquireValidatedPublication(ctx, next, &durable)
	if release != nil {
		release()
	}
	if !errors.Is(err, model.ErrFinalityChanged) || store.conflicted {
		t.Fatalf("changed generation was not retryable: %v fenced=%v", err, store.conflicted)
	}
}

func TestValidatedPublicationKeepsNilDurableAnchorExplicit(t *testing.T) {
	ctx := context.Background()
	r, rpc, store := testReader()
	if err := r.Check(ctx); err != nil {
		t.Fatal(err)
	}
	// A nil transaction anchor must not silently turn into a cached anchor read.
	// Simulate unavailable cached-anchor history while the candidate remains canonical.
	delete(rpc.headers, "0x6")
	next := model.Block{Number: 8, Hash: rpc.headers["0x8"].Hash(), Timestamp: 8}
	release, err := r.AcquireValidatedPublication(ctx, next, nil)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if store.conflicted {
		t.Fatal("nil durable snapshot permanently fenced")
	}
}

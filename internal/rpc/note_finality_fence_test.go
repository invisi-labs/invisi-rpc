package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-rpc/internal/finality"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	"github.com/invisi-labs/invisi-rpc/internal/storage"
)

type publicationFenceStore struct {
	anchor   *model.Block
	conflict bool
	readErr  error
}

func (s *publicationFenceStore) FinalizedAnchor(context.Context) (*model.Block, error) {
	return s.anchor, nil
}
func (s *publicationFenceStore) SaveFinalizedAnchor(_ context.Context, b model.Block) error {
	s.anchor = &b
	return nil
}
func (s *publicationFenceStore) FinalityConflict(context.Context) (bool, error) {
	return s.conflict, s.readErr
}
func (s *publicationFenceStore) MarkFinalityConflict(context.Context) error {
	s.conflict = true
	return nil
}

type publicationFenceRPC struct{ header *types.Header }

func (r *publicationFenceRPC) CallContext(_ context.Context, out any, _ string, _ ...any) error {
	raw, e := json.Marshal(r.header)
	if e != nil {
		return e
	}
	var obj map[string]any
	if e = json.Unmarshal(raw, &obj); e != nil {
		return e
	}
	obj["hash"] = r.header.Hash().Hex()
	raw, e = json.Marshal(obj)
	if e == nil {
		*out.(*json.RawMessage) = raw
	}
	return e
}

func TestPublicationSafeConfirmationMustRespectFinalizedFence(t *testing.T) {
	for _, kind := range []string{"conflict", "restart", "sqlite_restart", "during_confirmation", "store_error", "safe_only"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			api, s, chain := confirmedFixture(t)
			chain.header.Difficulty = big.NewInt(1)
			b := model.Block{Number: 10, Hash: chain.header.Hash(), Timestamp: 123, ParentHash: chain.header.ParentHash}
			s.head = &b
			s.lookup.Output.Block = b
			s.lookup.Membership.Root.Block = b
			chain.receipt.BlockHash = b.Hash
			anchorStore := &publicationFenceStore{}
			upstream := &publicationFenceRPC{header: chain.header}
			reader := &finality.Reader{RPC: upstream, Store: anchorStore}
			var disk *storage.SQLiteStore
			path := filepath.Join(t.TempDir(), "fence.sqlite")
			if kind == "sqlite_restart" {
				var err error
				disk, err = storage.OpenSQLite(ctx, path, api.Deployment)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { disk.Close() }()
				reader.Store = disk
			}
			if e := reader.Check(ctx); e != nil {
				t.Fatal(e)
			}
			fence := func() {
				fork := types.CopyHeader(chain.header)
				fork.Extra = []byte("conflicting finalized hash")
				upstream.header = fork
				if got := reader.Read(ctx); got.Finalized.Status != "inconsistent" {
					t.Fatalf("fixture did not fence: %+v", got)
				}
				upstream.header = chain.header
			}
			switch kind {
			case "conflict":
				fence()
			case "restart":
				fence()
				reader = &finality.Reader{RPC: upstream, Store: anchorStore}
			case "sqlite_restart":
				fence()
				disk.Close()
				var err error
				disk, err = storage.OpenSQLite(ctx, path, api.Deployment)
				if err != nil {
					t.Fatal(err)
				}
				reader = &finality.Reader{RPC: upstream, Store: disk}
			case "during_confirmation":
				chain.onRoot = fence
			case "store_error":
				anchorStore.readErr = errors.New("cannot read durable fence")
			case "safe_only":
				reader = &finality.Reader{RPC: safeOnlyConfirmationRPC{upstream}, Store: &publicationFenceStore{}}
			}
			api.Finality = reader
			got, e := api.confirmNote(ctx, s.lookup.Output.Commitment, "safe")
			if e != nil {
				t.Fatal(e)
			}
			if kind == "safe_only" {
				if got.Status != "confirmed" {
					t.Fatalf("safe-only chain lost support: %+v", got)
				}
				return
			}
			if got.Status != "pending" || got.Output != nil || got.Membership != nil || got.Confirmation != nil {
				t.Fatalf("confirmation escaped fence: %+v", got)
			}
			want := "finality_inconsistent"
			if kind == "store_error" {
				want = "finality_unavailable"
			}
			if got.Reason == nil || *got.Reason != want {
				t.Fatalf("wrong fence reason: %+v", got)
			}
		})
	}
}

type safeOnlyConfirmationRPC struct{ RPC finality.RPC }

func (r safeOnlyConfirmationRPC) CallContext(ctx context.Context, out any, method string, args ...any) error {
	if args[0] == "finalized" {
		return nil
	}
	return r.RPC.CallContext(ctx, out, method, args...)
}

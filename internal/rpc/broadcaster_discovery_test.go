package rpc

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	"github.com/invisi-labs/invisi-rpc/internal/storage"
)

func TestBroadcasterDiscoveryPagesUseCanonicalAnchor(t *testing.T) {
	ctx := context.Background()
	dep := model.Deployment{ChainID: "97", Protocol: common.HexToAddress("0x1"), FromBlock: 8, BroadcasterRegistry: &model.ContractDeployment{Address: common.HexToAddress("0x2"), RuntimeCodeHash: common.HexToHash("0x3"), FromBlock: 8}}
	store, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "discovery.db"), dep)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a, err := New(store, dep, "1")
	if err != nil {
		t.Fatal(err)
	}
	h, err := HTTPHandler(a)
	if err != nil {
		t.Fatal(err)
	}
	if r := call(t, h, "invisi_getBroadcasters", []any{map[string]any{}}, ""); r.Error == nil {
		t.Fatal("claimed complete empty index before first registry block")
	}
	first := common.HexToAddress("0x10")
	second := common.HexToAddress("0x20")
	off := false
	b := model.Batch{Block: model.Block{Number: 8, Hash: common.HexToHash("0x8"), Timestamp: 100}, Broadcasters: []model.BroadcasterUpdate{
		{Operator: first, Endpoint: "https://first.example", QuoteSigner: first, FeeRecipient: first, LogIndex: 1},
		{Operator: second, Endpoint: "https://second.example", QuoteSigner: second, FeeRecipient: second, LogIndex: 2},
	}}
	if err = store.Apply(ctx, b); err != nil {
		t.Fatal(err)
	}
	r := call(t, h, "invisi_getBroadcasters", []any{map[string]any{"enabled": true, "limit": 1}}, "")
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	var page model.BroadcasterPage
	if err = json.Unmarshal(r.Result, &page); err != nil {
		t.Fatal(err)
	}
	if page.Next == nil || *page.Next != first || len(page.Broadcasters) != 1 || page.Broadcasters[0].Block != b.Block {
		t.Fatalf("wrong first page %+v", page)
	}
	next := model.Batch{Block: model.Block{Number: 9, Hash: common.HexToHash("0x9"), ParentHash: b.Block.Hash, Timestamp: 101}, Broadcasters: []model.BroadcasterUpdate{{Operator: second, Enabled: &off, LogIndex: 1}}}
	if err = store.Apply(ctx, next); err != nil {
		t.Fatal(err)
	}
	// Continuing an anchored page must not skip a service disabled in a newer block.
	r = call(t, h, "invisi_getBroadcasters", []any{map[string]any{"enabled": true, "limit": 1, "after": page.Next, "block": page.Block}}, "")
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	if err = json.Unmarshal(r.Result, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Broadcasters) != 1 || page.Broadcasters[0].Operator != second || !page.Broadcasters[0].Enabled || page.Block != b.Block {
		t.Fatalf("mixed pages across indexed heads %+v", page)
	}
	if err = store.Rollback(ctx, 8); err != nil {
		t.Fatal(err)
	}
	stale := next.Block
	if r = call(t, h, "invisi_getBroadcasters", []any{map[string]any{"block": stale}}, ""); r.Error == nil {
		t.Fatal("accepted orphaned cursor anchor")
	}
	a.Deployment.BroadcasterRegistry = nil
	if _, err = a.GetBroadcasters(ctx, model.BroadcasterQuery{}); err == nil {
		t.Fatal("API ignored missing configured registry")
	}
}

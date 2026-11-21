package rpc

import (
	"context"
	"encoding/json"
	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	"github.com/invisi-labs/invisi-rpc/internal/storage"
	"path/filepath"
	"testing"
)

func TestCreditStatusWireBoundaries(t *testing.T) {
	ctx := context.Background()
	owner, token := common.HexToAddress("1"), common.HexToAddress("2")
	dep := model.Deployment{ChainID: "1", FromBlock: 10}
	s, e := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "wire.db"), dep)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	b := model.Batch{Block: model.Block{Number: 10, Timestamp: 100, Hash: common.HexToHash("10")}}
	if e = s.Apply(ctx, b); e != nil {
		t.Fatal(e)
	}
	a := &API{Store: s, Deployment: dep}
	h, e := HTTPHandler(a)
	if e != nil {
		t.Fatal(e)
	}
	r := call(t, h, "invisi_getCreditStatus", []any{owner, token, "340282366920938463463374607431768211455", 10}, "")
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	var out struct {
		Status *uint8
		Block  model.Block
	}
	if e = json.Unmarshal(r.Result, &out); e != nil || out.Status == nil || *out.Status != 0 || out.Block != b.Block {
		t.Fatalf("wire %s %v", r.Result, e)
	}
	for _, id := range []any{"0", "01", "+1", "1e2", "340282366920938463463374607431768211456", 1, nil} {
		r = call(t, h, "invisi_getCreditStatus", []any{owner, token, id, 10}, "")
		if r.Error == nil {
			t.Fatalf("accepted %v", id)
		}
	}
	for _, n := range []uint64{9, 11, 1 << 53} {
		if _, e = a.GetCreditStatus(ctx, owner, token, "1", n); e == nil {
			t.Fatalf("accepted block %d", n)
		}
	}
	for _, method := range []string{"invisi_getCreditAllocation", "invisi_getCreditRevocationBitmap"} {
		r = call(t, h, method, nil, "")
		if r.Error == nil || r.Error.Code != -32601 {
			t.Fatalf("retired method %s: %+v", method, r)
		}
	}
}

type statusChangingStore struct {
	*storage.SQLiteStore
	status uint8
	change func()
}

func (s *statusChangingStore) CreditStatusAt(context.Context, common.Address, common.Address, string, uint64) (uint8, error) {
	if s.change != nil {
		s.change()
	}
	return s.status, nil
}
func TestCreditStatusRejectsInvalidBackendAndReorg(t *testing.T) {
	ctx := context.Background()
	dep := model.Deployment{ChainID: "1", FromBlock: 10}
	s, e := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "fence.db"), dep)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	b := model.Batch{Block: model.Block{Number: 10, Timestamp: 10, Hash: common.HexToHash("10")}}
	if e = s.Apply(ctx, b); e != nil {
		t.Fatal(e)
	}
	wrapped := &statusChangingStore{SQLiteStore: s, status: 3}
	a := &API{Store: wrapped, Deployment: dep}
	owner, token := common.HexToAddress("1"), common.HexToAddress("2")
	if v, e := a.GetCreditStatus(ctx, owner, token, "1", 10); e == nil || v != nil {
		t.Fatal("accepted invalid status", v, e)
	}
	wrapped.status = 1
	wrapped.change = func() {
		if e = s.Rollback(ctx, 9); e != nil {
			t.Fatal(e)
		}
		b.Block.Hash = common.HexToHash("11")
		if e = s.Apply(ctx, b); e != nil {
			t.Fatal(e)
		}
	}
	if v, e := a.GetCreditStatus(ctx, owner, token, "1", 10); e == nil || v != nil {
		t.Fatal("accepted reorganized anchor", v, e)
	}
}

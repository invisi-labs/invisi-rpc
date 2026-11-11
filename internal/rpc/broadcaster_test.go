package rpc

import (
	"context"
	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/storage"
	"path/filepath"
	"testing"
)

func TestBroadcasterJSONRPC(t *testing.T) {
	dep := model.Deployment{ChainID: "97", Protocol: common.HexToAddress("0x1"), BroadcasterRegistry: &model.ContractDeployment{Address: common.HexToAddress("0x2"), RuntimeCodeHash: common.HexToHash("0x3")}}
	s, err := storage.OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "rpc.db"), dep)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Apply(context.Background(), model.Batch{Block: model.Block{Number: 0, Hash: common.HexToHash("0x8")}}); err != nil {
		t.Fatal(err)
	}
	a, err := New(s, dep, "1")
	if err != nil {
		t.Fatal(err)
	}
	h, err := HTTPHandler(a)
	if err != nil {
		t.Fatal(err)
	}
	r := call(t, h, "invisi_getBroadcasters", []any{map[string]any{"enabled": true}}, "")
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	for _, q := range []map[string]any{{"limit": 101}, {"limit": -1}, {"after": common.HexToAddress("0x1").Hex()}, {"after": "0xshort"}} {
		if r = call(t, h, "invisi_getBroadcasters", []any{q}, ""); r.Error == nil {
			t.Fatalf("accepted invalid query %+v", q)
		}
	}
}

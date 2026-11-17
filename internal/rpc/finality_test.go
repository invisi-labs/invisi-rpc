package rpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-rpc/internal/finality"
	"github.com/invisi-lab/invisi-rpc/internal/model"
)

type fixedFinality struct{ value finality.Snapshot }

func (f fixedFinality) Read(context.Context) finality.Snapshot { return f.value }
func TestStatusNativeFinalityIndependentOfIndexLag(t *testing.T) {
	for _, indexed := range []*model.Block{nil, {Number: 2, Hash: common.HexToHash("0x2")}} {
		a, err := New(&fakeBackend{head: indexed}, model.Deployment{ChainID: "31337", Protocol: common.HexToAddress("1234"), FromBlock: 8}, "1")
		if err != nil {
			t.Fatal(err)
		}
		f := finality.Unavailable()
		f.Finalized = finality.Head{Status: "available", Block: &model.Block{Number: 100, Timestamp: 999, Hash: common.HexToHash("0x100")}}
		a.Finality = fixedFinality{f}
		h, err := HTTPHandler(a)
		if err != nil {
			t.Fatal(err)
		}
		response := call(t, h, "invisi_getStatus", []any{}, "")
		var got Status
		if err = json.Unmarshal(response.Result, &got); err != nil {
			t.Fatal(err)
		}
		if got.Finality.Finalized.Status != "available" || got.Finality.Finalized.Block.Number != 100 || got.Finality.Safe.Status != "unavailable" {
			t.Fatal(string(response.Result))
		}
		if (indexed == nil) != (got.IndexedHead == nil) {
			t.Fatal("index head changed")
		}
	}
}

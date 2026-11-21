package protocol

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	"testing"
)

func TestLegacyCreditLogFailsExplicitly(t *testing.T) {
	d, e := NewDecoder()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = d.Decode(model.Block{}, []types.Log{{Topics: []common.Hash{common.HexToHash("0x3ff78f0284d33ce5183bc7c13bbedfddbf90722c4b3d90a5b8e59da9bbd70565")}}}); e == nil {
		t.Fatal("silently accepted legacy CreditCreated log")
	}
}

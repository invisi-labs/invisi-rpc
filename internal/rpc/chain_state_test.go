package rpc

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/invisi-labs/invisi-rpc/internal/model"
)

type emptyBytesStateFake struct {
	stateFake
	data []byte
}

func (f *emptyBytesStateFake) CodeAt(context.Context, common.Address, *big.Int) ([]byte, error) {
	return f.data, nil
}

func (f *emptyBytesStateFake) CallContract(context.Context, ethereum.CallMsg, *big.Int) ([]byte, error) {
	return f.data, nil
}

func TestChainStateEmptyBytesJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{{name: "nil"}, {name: "empty", data: []byte{}}} {
		for _, kind := range []string{"code", "call"} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				a := &API{ChainState: &emptyBytesStateFake{data: tc.data}, Deployment: model.Deployment{Protocol: common.HexToAddress("1")}}
				h, err := HTTPHandler(a)
				if err != nil {
					t.Fatal(err)
				}
				r := call(t, h, "invisi_readChainState", []any{ChainStateRequest{
					Kind: kind, Address: a.Deployment.Protocol, Data: crypto.Keccak256([]byte("VERSION()"))[:4],
				}}, "")
				if r.Error != nil {
					t.Fatalf("RPC error: %+v", r.Error)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(r.Result, &fields); err != nil {
					t.Fatal(err)
				}
				if string(fields["data"]) != `"0x"` {
					t.Fatalf("empty bytes must serialize as data=\"0x\": %s", r.Result)
				}
			})
		}
	}
}

type stateFake struct {
	calls int
	h     *types.Header
	r     *types.Receipt
}

func (f *stateFake) HeaderByNumber(context.Context, *big.Int) (*types.Header, error) { return f.h, nil }
func (f *stateFake) CodeAt(context.Context, common.Address, *big.Int) ([]byte, error) {
	return []byte{1}, nil
}
func (f *stateFake) CallContract(context.Context, ethereum.CallMsg, *big.Int) ([]byte, error) {
	f.calls++
	return []byte{1}, nil
}
func (f *stateFake) TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error) {
	return f.r, nil
}
func TestChainStateReadBoundaries(t *testing.T) {
	f := &stateFake{h: &types.Header{Number: big.NewInt(10)}}
	a := &API{ChainState: f, Deployment: model.Deployment{Protocol: common.HexToAddress("1")}}
	ctx := context.Background()
	for _, r := range []ChainStateRequest{{Kind: "send"}, {Kind: "call", Address: a.Deployment.Protocol, Data: []byte{1, 2, 3, 4}}, {Kind: "call", Address: common.HexToAddress("2"), Data: crypto.Keccak256([]byte("transfer(address,uint256)"))[:4]}, {Kind: "header", Block: "pending"}, {Kind: "call", Gas: 30_000_001}} {
		if _, e := a.ReadChainState(ctx, r); e == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
	if f.calls != 0 {
		t.Fatal("rejected requests reached upstream")
	}
	data := crypto.Keccak256([]byte("VERSION()"))[:4]
	if _, e := a.ReadChainState(ctx, ChainStateRequest{Kind: "call", Address: a.Deployment.Protocol, Data: data}); e != nil {
		t.Fatal(e)
	}
	data = append(data, 0)
	if _, e := a.ReadChainState(ctx, ChainStateRequest{Kind: "call", Address: a.Deployment.Protocol, Data: data}); e == nil {
		t.Fatal("accepted trailing calldata")
	}
	h, e := a.ReadChainState(ctx, ChainStateRequest{Kind: "header", Block: "0xa"})
	if e != nil || h.HeaderHash != f.h.Hash() {
		t.Fatal("header not bound", e)
	}
	if _, e = a.ReadChainState(ctx, ChainStateRequest{Kind: "header", Block: "0xb"}); e == nil {
		t.Fatal("accepted wrong header")
	}
	hash := common.HexToHash("3")
	f.r = &types.Receipt{TxHash: hash, BlockNumber: big.NewInt(10), BlockHash: common.HexToHash("4")}
	if _, e = a.ReadChainState(ctx, ChainStateRequest{Kind: "receipt", TransactionHash: hash}); e == nil {
		t.Fatal("accepted noncanonical receipt")
	}
	f.r = nil
	if v, e := a.ReadChainState(ctx, ChainStateRequest{Kind: "receipt", TransactionHash: hash}); e != nil || v.Receipt != nil {
		t.Fatal(e)
	}
}

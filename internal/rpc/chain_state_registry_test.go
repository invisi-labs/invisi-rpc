package rpc

import (
	"bytes"
	"context"
	"math/big"
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	"github.com/invisi-labs/invisi-sdk-go/protocol/contracts"
)

type registryStateFake struct {
	stateFake
	message ethereum.CallMsg
	block   *big.Int
}

func (f *registryStateFake) CallContract(_ context.Context, msg ethereum.CallMsg, block *big.Int) ([]byte, error) {
	f.calls++
	f.message, f.block = msg, block
	return []byte{0xab, 0xcd}, nil
}

// This catches selector drift from the SDK and loss of the pinned call arguments.
func TestChainStateRegistrySDKReads(t *testing.T) {
	ab, err := contracts.InvisiAddressRegistryMetaData.GetAbi()
	if err != nil {
		t.Fatal(err)
	}
	registry := common.HexToAddress("0x2222")
	owner := common.HexToAddress("0x1234")
	identity := common.BytesToHash(owner.Bytes()) // Core's nonzero uint160 owner, zero-padded to bytes32.
	for _, name := range []string{"isRegistered", "nonceOf", "receivingKeysOf", "CHAIN_ID"} {
		t.Run(name, func(t *testing.T) {
			var args []any
			if name != "CHAIN_ID" {
				args = []any{identity}
			}
			data, err := ab.Pack(name, args...)
			if err != nil {
				t.Fatal(err)
			}
			f := &registryStateFake{}
			a := &API{ChainState: f, Deployment: model.Deployment{Protocol: common.HexToAddress("0x1111"), Registry: &model.ContractDeployment{Address: registry}}}
			r, err := a.ReadChainState(context.Background(), ChainStateRequest{Kind: "call", Block: "0x2a", Address: registry, From: owner, Gas: 123456, Data: data})
			if err != nil {
				t.Fatalf("canonical SDK %s rejected: %v", ab.Methods[name].Sig, err)
			}
			if f.calls != 1 || f.message.To == nil || *f.message.To != registry || f.message.From != owner || f.message.Gas != 123456 || !bytes.Equal(f.message.Data, data) || f.block == nil || f.block.Cmp(big.NewInt(42)) != 0 {
				t.Fatalf("registry call lost its pinned arguments: %+v block=%v calls=%d", f.message, f.block, f.calls)
			}
			if !bytes.Equal(r.Data, []byte{0xab, 0xcd}) || r.Deployment.Registry.Address != registry {
				t.Fatalf("registry response lost upstream bytes or deployment: %+v", r)
			}
		})
	}
}

// This catches broad ABI allowlisting, permissive lengths and obsolete address selectors.
func TestChainStateRegistryRejectsDisallowedCalls(t *testing.T) {
	ab, err := contracts.InvisiAddressRegistryMetaData.GetAbi()
	if err != nil {
		t.Fatal(err)
	}
	registry := common.HexToAddress("0x2222")
	protocol := common.HexToAddress("0x1111")
	identity := common.HexToHash("0x1234")
	f := &registryStateFake{}
	a := &API{ChainState: f, Deployment: model.Deployment{Protocol: protocol, Registry: &model.ContractDeployment{Address: registry}}}
	reject := func(t *testing.T, target common.Address, data []byte) {
		t.Helper()
		before := f.calls
		if _, err := a.ReadChainState(context.Background(), ChainStateRequest{Kind: "call", Address: target, Data: data}); err == nil {
			t.Fatal("disallowed registry call accepted")
		}
		if f.calls != before {
			t.Fatal("disallowed registry call reached upstream")
		}
	}
	for name, method := range ab.Methods {
		var args []any
		for _, input := range method.Inputs {
			args = append(args, reflect.Zero(input.Type.GetType()).Interface())
		}
		allowed := name == "isRegistered" || name == "nonceOf" || name == "receivingKeysOf" || name == "CHAIN_ID"
		if allowed && name != "CHAIN_ID" {
			args = []any{identity}
		}
		data, err := ab.Pack(name, args...)
		if err != nil {
			t.Fatalf("pack SDK %s: %v", name, err)
		}
		if !allowed {
			t.Run(name, func(t *testing.T) { reject(t, registry, data) })
			continue
		}
		for _, tc := range []struct {
			name   string
			target common.Address
			data   []byte
		}{
			{"short_selector", registry, data[:3]},
			{"truncated", registry, data[:len(data)-1]},
			{"trailing_byte", registry, append(append([]byte{}, data...), 0)},
			{"trailing_word", registry, append(append([]byte{}, data...), make([]byte, 32)...)},
			{"wrong_protocol_target", protocol, data},
			{"unrelated_target", common.HexToAddress("0x3333"), data},
			{"zero_target", common.Address{}, data},
		} {
			t.Run(name+"/"+tc.name, func(t *testing.T) { reject(t, tc.target, tc.data) })
		}
		if name != "CHAIN_ID" {
			old := append(crypto.Keccak256([]byte(name + "(address)"))[:4], identity.Bytes()...)
			t.Run(name+"/obsolete_address_selector", func(t *testing.T) { reject(t, registry, old) })
		}
	}
	data, err := ab.Pack("isRegistered", identity)
	if err != nil {
		t.Fatal(err)
	}
	a.Deployment.Registry = nil
	t.Run("missing_registry_pin", func(t *testing.T) { reject(t, registry, data) })
}

// Read queries preserve the bytes32 ABI domain; owner restrictions apply to writes.
func TestChainStateRegistryBytes32IdentityReads(t *testing.T) {
	ab, err := contracts.InvisiAddressRegistryMetaData.GetAbi()
	if err != nil {
		t.Fatal(err)
	}
	registry := common.HexToAddress("0x2222")
	for _, identity := range []common.Hash{{}, {0: 0xff, 30: 0x12, 31: 0x34}} {
		for _, name := range []string{"isRegistered", "nonceOf", "receivingKeysOf"} {
			t.Run(name+"/"+identity.Hex(), func(t *testing.T) {
				data, err := ab.Pack(name, identity)
				if err != nil {
					t.Fatal(err)
				}
				f := &registryStateFake{}
				a := &API{ChainState: f, Deployment: model.Deployment{Registry: &model.ContractDeployment{Address: registry}}}
				if _, err := a.ReadChainState(context.Background(), ChainStateRequest{Kind: "call", Address: registry, Data: data}); err != nil {
					t.Fatalf("bytes32 read rejected: %v", err)
				}
				if f.calls != 1 || !bytes.Equal(f.message.Data, data) {
					t.Fatal("bytes32 identity not preserved upstream")
				}
			})
		}
	}
}

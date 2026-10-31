package protocol

import (
	"bytes"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/cryptography"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
	"testing"
)

func TestRegistryEventsBoundToDeployment(t *testing.T) {
	owner := common.HexToAddress("0x1234")
	dep := model.Deployment{Protocol: common.HexToAddress("0x1"), FromBlock: 10, Registry: &model.ContractDeployment{Address: common.HexToAddress("0x2"), FromBlock: 8}}
	d, err := NewDeploymentDecoder(dep)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := abi.JSON(bytes.NewReader(registryABI))
	if err != nil {
		t.Fatal(err)
	}
	event := parsed.Events["ReceivingKeysSet"]
	detection := make([]byte, 32)
	detection[0] = 1
	clue, err := cryptography.DeriveClueKey(detection)
	if err != nil {
		t.Fatal(err)
	}
	data, err := event.Inputs.NonIndexed().Pack(common.FromHex("0x0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"), common.BytesToHash(clue), uint64(18446744073709551615))
	if err != nil {
		t.Fatal(err)
	}
	log := types.Log{Address: dep.Registry.Address, Topics: []common.Hash{event.ID, common.BytesToHash(owner.Bytes())}, Data: data, Index: 5}
	batch, err := d.Decode(model.Block{Number: 8}, []types.Log{log})
	if err != nil || len(batch.Registrations) != 1 || batch.Registrations[0].Nonce != 18446744073709551615 || batch.Registrations[0].WalletIdentity != owner {
		t.Fatalf("decode: %+v %v", batch, err)
	}
	if _, err = d.Decode(model.Block{Number: 7}, []types.Log{log}); err == nil {
		t.Fatal("accepted event before deployment")
	}
	other := log
	other.Address = common.HexToAddress("0x3")
	if _, err = d.Decode(model.Block{Number: 10}, []types.Log{other}); err == nil {
		t.Fatal("accepted another emitter")
	}
	malformed := log
	malformed.Data = []byte{1}
	if _, err = d.Decode(model.Block{Number: 8}, []types.Log{malformed}); err == nil {
		t.Fatal("accepted malformed data")
	}
}

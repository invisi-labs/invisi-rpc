package protocol

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-lab/invisi-rpc/internal/model"
)

func TestBroadcasterEvents(t *testing.T) {
	dep := model.Deployment{Protocol: common.HexToAddress("0x1"), FromBlock: 10, BroadcasterRegistry: &model.ContractDeployment{Address: common.HexToAddress("0x2"), FromBlock: 8}}
	d, err := NewDeploymentDecoder(dep)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := abi.JSON(bytes.NewReader(broadcasterABI))
	set := a.Events["BroadcasterSet"]
	availability := a.Events["AvailabilitySet"]
	operator := common.HexToAddress("0x1234")
	signer := common.HexToAddress("0x22")
	recipient := common.HexToAddress("0x33")
	data, _ := set.Inputs.NonIndexed().Pack("https://relay.example", "", signer, recipient)
	off, _ := availability.Inputs.NonIndexed().Pack(false)
	logs := []types.Log{{Address: dep.BroadcasterRegistry.Address, Topics: []common.Hash{set.ID, common.BytesToHash(operator.Bytes())}, Data: data, Index: 1}, {Address: dep.BroadcasterRegistry.Address, Topics: []common.Hash{availability.ID, common.BytesToHash(operator.Bytes())}, Data: off, Index: 2}}
	batch, err := d.Decode(model.Block{Number: 8}, logs)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Broadcasters) != 2 || batch.Broadcasters[0].Operator != operator || batch.Broadcasters[1].Enabled == nil || *batch.Broadcasters[1].Enabled {
		t.Fatalf("wrong updates: %+v", batch.Broadcasters)
	}
	if _, err = d.Decode(model.Block{Number: 7}, logs); err == nil {
		t.Fatal("accepted before registry deployment")
	}
	logs[0].Topics[1][0] = 1
	if _, err = d.Decode(model.Block{Number: 8}, logs); err == nil {
		t.Fatal("accepted noncanonical operator")
	}
}

func broadcasterFixture(t *testing.T) (*DeploymentDecoder, model.Deployment, types.Log) {
	t.Helper()
	dep := model.Deployment{Protocol: common.HexToAddress("0x1"), FromBlock: 10, BroadcasterRegistry: &model.ContractDeployment{Address: common.HexToAddress("0x2"), FromBlock: 8}}
	d, e := NewDeploymentDecoder(dep)
	if e != nil {
		t.Fatal(e)
	}
	event := d.broadcaster.Events["BroadcasterSet"]
	data, e := event.Inputs.NonIndexed().Pack("https://relay.example", "ipfs://metadata", common.HexToAddress("0x22"), common.HexToAddress("0x33"))
	if e != nil {
		t.Fatal(e)
	}
	return d, dep, types.Log{Address: dep.BroadcasterRegistry.Address, Topics: []common.Hash{event.ID, common.BytesToHash(common.HexToAddress("0x1234").Bytes())}, Data: data, Index: 3}
}
func TestBroadcasterStrictCanonicalEncoding(t *testing.T) {
	d, _, log := broadcasterFixture(t)
	mutations := []func(*types.Log){func(l *types.Log) { l.Data = append(l.Data, make([]byte, 32)...) }, func(l *types.Log) { l.Data[len(l.Data)-1] = 1 }, func(l *types.Log) { l.Data[64] = 1 }, func(l *types.Log) { l.Topics = append(l.Topics, common.Hash{}) }, func(l *types.Log) { l.Topics[1][0] = 1 }, func(l *types.Log) { l.Removed = true }}
	for i, mutate := range mutations {
		bad := log
		bad.Data = bytes.Clone(log.Data)
		bad.Topics = append([]common.Hash(nil), log.Topics...)
		mutate(&bad)
		if _, e := d.Decode(model.Block{Number: 10}, []types.Log{bad}); e == nil {
			t.Fatalf("accepted malformed registry encoding %d", i)
		}
	}
}
func TestBroadcasterRejectsWrongConfiguredEmitterAndDuplicateIndex(t *testing.T) {
	d, dep, log := broadcasterFixture(t)
	bad := log
	bad.Address = dep.Protocol
	if _, e := d.Decode(model.Block{Number: 10}, []types.Log{bad}); e == nil {
		t.Fatal("accepted registry signature from protocol emitter")
	}
	if _, e := d.Decode(model.Block{Number: 10}, []types.Log{log, log}); e == nil {
		t.Fatal("accepted duplicate log index")
	}
}
func TestBroadcasterSameBlockUpdatesStayOrdered(t *testing.T) {
	d, _, set := broadcasterFixture(t)
	event := d.broadcaster.Events["AvailabilitySet"]
	off, _ := event.Inputs.NonIndexed().Pack(false)
	on, _ := event.Inputs.NonIndexed().Pack(true)
	disable := set
	disable.Index = 4
	disable.Topics = append([]common.Hash{event.ID}, set.Topics[1])
	disable.Data = off
	change := set
	change.Index = 5
	enable := disable
	enable.Index = 6
	enable.Data = on
	batch, e := d.Decode(model.Block{Number: 10}, []types.Log{enable, change, set, disable})
	if e != nil {
		t.Fatal(e)
	}
	if len(batch.Broadcasters) != 4 || len(batch.Events) != 4 {
		t.Fatal("lost updates")
	}
	for i, u := range batch.Broadcasters {
		if u.LogIndex != uint(i+3) || batch.Events[i].LogIndex != u.LogIndex {
			t.Fatal("updates out of order")
		}
	}
	if batch.Broadcasters[0].Enabled != nil || batch.Broadcasters[2].Enabled != nil || *batch.Broadcasters[1].Enabled || !*batch.Broadcasters[3].Enabled {
		t.Fatal("wrong update semantics")
	}
}

func TestBroadcasterAvailabilityRequiresCanonicalBoolean(t *testing.T) {
	d, _, log := broadcasterFixture(t)
	event := d.broadcaster.Events["AvailabilitySet"]
	log.Topics[0] = event.ID
	data, e := event.Inputs.NonIndexed().Pack(true)
	if e != nil {
		t.Fatal(e)
	}
	for _, bad := range [][]byte{append(bytes.Clone(data), 0), append(bytes.Clone(data), make([]byte, 32)...), append(make([]byte, 31), 2)} {
		log.Data = bad
		if _, e = d.Decode(model.Block{Number: 10}, []types.Log{log}); e == nil {
			t.Fatal("accepted noncanonical availability boolean")
		}
	}
}

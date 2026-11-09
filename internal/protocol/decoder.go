// Package protocol decodes events using the current built Protocol ABI.
package protocol

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"sort"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
)

//go:generate go run generate_protocol_abi.go
//go:embed InvisiProtocol.abi
var pinnedABI []byte

type Decoder struct{ abi abi.ABI }

func NewDecoder() (*Decoder, error) {
	a, e := abi.JSON(bytes.NewReader(pinnedABI))
	return &Decoder{abi: a}, e
}

type delivery struct {
	Ciphertext []byte
	Clue       []byte
}

func tuple[T any](v any) T { return *abi.ConvertType(v, new(T)).(*T) }
func hashes(v any) []common.Hash {
	a := v.([][32]byte)
	r := make([]common.Hash, len(a))
	for i := range a {
		r[i] = a[i]
	}
	return r
}
func tree(v any) (uint64, error) {
	n, ok := v.(*big.Int)
	if !ok || !n.IsUint64() {
		return 0, fmt.Errorf("tree ID exceeds uint64")
	}
	return n.Uint64(), nil
}
func fields(v any) map[string]any {
	r := map[string]any{}
	x := reflect.ValueOf(v)
	t := x.Type()
	for i := 0; i < x.NumField(); i++ {
		n := t.Field(i).Name
		n = string(bytes.ToLower([]byte(n[:1]))) + n[1:]
		r[n] = x.Field(i).Interface()
	}
	return r
}

func (d *Decoder) Decode(block model.Block, logs []types.Log) (batch model.Batch, err error) {
	batch = model.Batch{Block: block, Logs: append([]types.Log(nil), logs...)}
	// ABI conversion failures are malformed logs, never process crashes.
	defer func() {
		if r := recover(); r != nil {
			batch = model.Batch{}
			err = fmt.Errorf("malformed protocol event: %v", r)
		}
	}()
	ordered := append([]types.Log(nil), logs...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Index < ordered[j].Index })
	type decoded struct {
		log    types.Log
		name   string
		values map[string]any
	}
	events := []decoded{}
	type location struct {
		tx   common.Hash
		tree uint64
		leaf uint32
	}
	inserted := map[location]common.Hash{}
	insertionLengths := map[location]int{}
	for _, l := range ordered {
		if l.Removed {
			return model.Batch{}, fmt.Errorf("removed log %d", l.Index)
		}
		if len(l.Topics) == 0 {
			continue
		}
		e, eerr := d.abi.EventByID(l.Topics[0])
		if eerr != nil {
			raw, _ := json.Marshal(eventJSON(map[string]any{"topics": l.Topics, "data": l.Data}))
			batch.Events = append(batch.Events, model.Event{Block: block, Name: "Unknown", TransactionHash: l.TxHash, LogIndex: l.Index, Data: raw})
			continue
		}
		m := map[string]any{}
		if err = e.Inputs.NonIndexed().UnpackIntoMap(m, l.Data); err != nil {
			return model.Batch{}, fmt.Errorf("%s log %d: %w", e.Name, l.Index, err)
		}
		indexed := abi.Arguments{}
		for _, a := range e.Inputs {
			if a.Indexed {
				indexed = append(indexed, a)
			}
		}
		if len(l.Topics) != len(indexed)+1 {
			return model.Batch{}, fmt.Errorf("%s: incorrect topic count", e.Name)
		}
		if err = abi.ParseTopicsIntoMap(m, indexed, l.Topics[1:]); err != nil {
			return model.Batch{}, err
		}
		raw, eerr := json.Marshal(eventJSON(m))
		if eerr != nil {
			return model.Batch{}, eerr
		}
		batch.Events = append(batch.Events, model.Event{Block: block, Name: e.Name, TransactionHash: l.TxHash, LogIndex: l.Index, Data: raw})
		events = append(events, decoded{l, e.Name, m})
		if e.Name == "TreeCreated" {
			id, x := tree(m["treeId"])
			if x != nil {
				return model.Batch{}, x
			}
			batch.CreatedTrees = append(batch.CreatedTrees, id)
		}
		if e.Name == "CommitmentsInserted" || e.Name == "BatchOutputs" {
			id, x := tree(m["treeId"])
			if x != nil {
				return model.Batch{}, x
			}
			start := m["startLeafIndex"].(uint32)
			var cs []common.Hash
			switch e.Name {
			case "CommitmentsInserted":
				cs = hashes(m["commitments"])
			case "BatchOutputs":
				cs = hashes(m["noteCommitments"])
			}
			if len(cs) == 0 || uint64(start)+uint64(len(cs)) > 65536 {
				return model.Batch{}, fmt.Errorf("%s: invalid insertion range", e.Name)
			}
			batch.Insertions = append(batch.Insertions, model.Insertion{TreeID: id, Start: start, Commitments: cs, LogIndex: l.Index})
			insertionLengths[location{l.TxHash, id, start}] = len(cs)
			for i, c := range cs {
				key := location{l.TxHash, id, start + uint32(i)}
				if _, ok := inserted[key]; ok {
					return model.Batch{}, fmt.Errorf("duplicate inserted leaf")
				}
				inserted[key] = c
			}
		}
	}
	for _, e := range events {
		m, l := e.values, e.log
		addNull := func(v any) { batch.Nullifiers = append(batch.Nullifiers, hashes(v)...) }
		add := func(id uint64, leaf uint32, c common.Hash, token common.Address, v delivery) error {
			if actual, ok := inserted[location{l.TxHash, id, leaf}]; !ok || actual != c {
				return fmt.Errorf("%s: output does not match transaction insertion", e.name)
			}
			batch.Outputs = append(batch.Outputs, model.NoteOutput{Block: block, TreeID: id, LeafIndex: leaf, Commitment: c, Token: token, Ciphertext: append([]byte(nil), v.Ciphertext...), Clue: append([]byte(nil), v.Clue...), TransactionHash: l.TxHash, LogIndex: l.Index, Kind: e.name})
			return nil
		}
		switch e.name {
		case "BatchOutputs":
			id, x := tree(m["treeId"])
			if x != nil {
				return model.Batch{}, x
			}
			cs := hashes(m["noteCommitments"])
			ts := m["tokens"].([]common.Address)
			p := m["packedDeliveries"].([]byte)
			if len(ts) != len(cs) {
				return model.Batch{}, fmt.Errorf("BatchOutputs: array lengths differ")
			}
			cursor := 0
			for i, c := range cs {
				if len(p)-cursor < 2 {
					return model.Batch{}, fmt.Errorf("BatchOutputs: truncated envelope length")
				}
				size := int(binary.BigEndian.Uint16(p[cursor : cursor+2]))
				cursor += 2
				if size == 0 {
					if err = add(id, m["startLeafIndex"].(uint32)+uint32(i), c, ts[i], delivery{}); err != nil {
						return model.Batch{}, err
					}
					continue
				}
				if size > 4096 || len(p)-cursor < size+68 {
					return model.Batch{}, fmt.Errorf("BatchOutputs: invalid envelope length")
				}
				cipher, clue := p[cursor:cursor+size], p[cursor+size:cursor+size+68]
				// Ciphertext versions and payload schemas are interpreted only by wallets.
				if clue[64] != 24 {
					return model.Batch{}, fmt.Errorf("BatchOutputs: invalid clue precision")
				}
				cursor += size + 68
				if err = add(id, m["startLeafIndex"].(uint32)+uint32(i), c, ts[i], delivery{cipher, clue}); err != nil {
					return model.Batch{}, err
				}
			}
			if cursor != len(p) {
				return model.Batch{}, fmt.Errorf("BatchOutputs: trailing delivery bytes")
			}
		case "Shield":
			id, x := tree(m["treeId"])
			if x != nil {
				return model.Batch{}, x
			}
			vs := tuple[[]delivery](m["deliveries"])
			start := m["startLeafIndex"].(uint32)
			if n, ok := insertionLengths[location{l.TxHash, id, start}]; !ok || n != len(vs) {
				return model.Batch{}, fmt.Errorf("Shield: deliveries do not match insertion length")
			}
			for i, v := range vs {
				leaf := start + uint32(i)
				c, ok := inserted[location{l.TxHash, id, leaf}]
				if !ok {
					return model.Batch{}, fmt.Errorf("Shield: missing transaction insertion")
				}
				if err = add(id, leaf, c, m["token"].(common.Address), v); err != nil {
					return model.Batch{}, err
				}
			}
		case "Transact", "CrossTreeTransact":
			addNull(m["nullifiers"])
			id, x := tree(m["outputTreeId"])
			if x != nil {
				return model.Batch{}, x
			}
			cs := hashes(m["noteCommitments"])
			ls := m["leafIndices"].([]uint32)
			vs := tuple[[]delivery](m["deliveries"])
			if len(cs) != len(ls) || len(cs) != len(vs) {
				return model.Batch{}, fmt.Errorf("%s: array lengths differ", e.name)
			}
			for i, c := range cs {
				if err = add(id, ls[i], c, m["token"].(common.Address), vs[i]); err != nil {
					return model.Batch{}, err
				}
			}
		case "BatchRequest":
			addNull(m["nullifiers"])
		case "CreditCreated", "CreditUpdated":
			p := m
			if e.name == "CreditUpdated" {
				p = fields(m["params"])
				batch.CreditNullifiers = append(batch.CreditNullifiers, p["nullifier"].([32]byte))
			}
			id, x := tree(m["outputTreeId"])
			if x != nil {
				return model.Batch{}, x
			}
			c := common.Hash(p["creditCommitment"].([32]byte))
			leaf := m["leafIndex"].(uint32)
			vs := tuple[[]delivery](m["deliveries"])
			if len(vs) != 2 {
				return model.Batch{}, fmt.Errorf("%s: expected owner and spender state deliveries", e.name)
			}
			for _, v := range vs {
				if err = add(id, leaf, c, p["token"].(common.Address), v); err != nil {
					return model.Batch{}, err
				}
				last := len(batch.Outputs) - 1
				batch.CreditStates = append(batch.CreditStates, model.StateFromOutput(batch.Outputs[last], []model.Delivery{{Ciphertext: v.Ciphertext, Clue: v.Clue}}))
				batch.Outputs = batch.Outputs[:last]
			}
		case "CreditDraw":
			p := fields(m["params"])
			ns := hashes(p["nullifiers"])
			batch.CreditNullifiers = append(batch.CreditNullifiers, ns...)
			cs := hashes(p["creditCommitments"])
			ls := m["leafIndices"].([]uint32)
			vs := tuple[[]delivery](m["deliveries"])
			n := len(cs)
			if n == 0 || len(ns) != n || len(ls) != n+1 || len(vs) != 2*n+1 || len(hashes(p["recoveryAmounts"])) != n {
				return model.Batch{}, fmt.Errorf("CreditDraw: inconsistent state and Note dimensions")
			}
			id, x := tree(m["outputTreeId"])
			if x != nil {
				return model.Batch{}, x
			}
			for i, c := range cs {
				if c == (common.Hash{}) {
					return model.Batch{}, fmt.Errorf("CreditDraw: zero successor state")
				}
				for _, v := range vs[2*i : 2*i+2] {
					if err = add(id, ls[i], c, p["token"].(common.Address), v); err != nil {
						return model.Batch{}, err
					}
					last := len(batch.Outputs) - 1
					batch.CreditStates = append(batch.CreditStates, model.StateFromOutput(batch.Outputs[last], []model.Delivery{{Ciphertext: v.Ciphertext, Clue: v.Clue}}))
					batch.Outputs = batch.Outputs[:last]
				}
			}
			if err = add(id, ls[n], common.Hash(p["noteCommitment"].([32]byte)), p["token"].(common.Address), vs[2*n]); err != nil {
				return model.Batch{}, err
			}

		}
	}
	combined := []model.CreditState{}
	for _, state := range batch.CreditStates {
		if len(combined) > 0 {
			last := &combined[len(combined)-1]
			if last.TransactionHash == state.TransactionHash && last.TreeID == state.TreeID && last.LeafIndex == state.LeafIndex {
				last.Deliveries = append(last.Deliveries, state.Deliveries...)
				continue
			}
		}
		combined = append(combined, state)
	}
	for _, state := range combined {
		if len(state.Deliveries) != 2 {
			return model.Batch{}, fmt.Errorf("Credit state must have exactly two deliveries")
		}
	}
	batch.CreditStates = combined
	return batch, nil
}

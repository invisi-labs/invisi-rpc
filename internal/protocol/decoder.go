// Package protocol decodes the ABI pinned to contract commit af770217a8cf4bd12fbf119455b4a51e16414bcb.
package protocol

import (
	"bytes"
	_ "embed"
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
			batch.Events = append(batch.Events, model.Event{Name: "Unknown", TransactionHash: l.TxHash, LogIndex: l.Index, Data: raw})
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
		batch.Events = append(batch.Events, model.Event{Name: e.Name, TransactionHash: l.TxHash, LogIndex: l.Index, Data: raw})
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
			cs := hashes(m["commitments"])
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
			batch.Outputs = append(batch.Outputs, model.Output{TreeID: id, LeafIndex: leaf, Commitment: c, Token: token, Ciphertext: append([]byte(nil), v.Ciphertext...), Clue: append([]byte(nil), v.Clue...), TransactionHash: l.TxHash, LogIndex: l.Index, Kind: e.name})
			return nil
		}
		switch e.name {
		case "BatchOutputs":
			id, x := tree(m["treeId"])
			if x != nil {
				return model.Batch{}, x
			}
			cs := hashes(m["commitments"])
			ts := m["tokens"].([]common.Address)
			p := m["packedDeliveries"].([]byte)
			if len(ts) != len(cs) || len(p) != 226*len(cs) {
				return model.Batch{}, fmt.Errorf("BatchOutputs: array lengths differ")
			}
			for i, c := range cs {
				if err = add(id, m["startLeafIndex"].(uint32)+uint32(i), c, ts[i], delivery{p[226*i : 226*i+158], p[226*i+158 : 226*(i+1)]}); err != nil {
					return model.Batch{}, err
				}
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
			cs := hashes(m["outputCommitments"])
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
		case "CreditCancelled":
			batch.Nullifiers = append(batch.Nullifiers, m["nullifier"].([32]byte))
		case "CreditCreated", "CreditToppedUp":
			p := m
			if e.name == "CreditToppedUp" {
				q := fields(m["params"])
				p = fields(q["creation"])
				batch.Nullifiers = append(batch.Nullifiers, q["creditNullifier"].([32]byte))
			}
			addNull(p["nullifiers"])
			id, x := tree(m["outputTreeId"])
			if x != nil {
				return model.Batch{}, x
			}
			ls := m["leafIndices"].([]uint32)
			vs := tuple[[]delivery](m["deliveries"])
			credit := common.Hash(p["creditCommitment"].([32]byte))
			change := common.Hash(p["changeCommitment"].([32]byte))
			want := 1
			if change != (common.Hash{}) {
				want = 2
			}
			if len(ls) != want || len(vs) != 3 {
				return model.Batch{}, fmt.Errorf("%s: array lengths differ", e.name)
			}
			for i := 0; i < 2; i++ {
				if err = add(id, ls[0], credit, p["token"].(common.Address), vs[i]); err != nil {
					return model.Batch{}, err
				}
			}
			if want == 2 {
				if err = add(id, ls[1], change, p["token"].(common.Address), vs[2]); err != nil {
					return model.Batch{}, err
				}
			} else if len(vs[2].Ciphertext) != 0 || len(vs[2].Clue) != 0 {
				return model.Batch{}, fmt.Errorf("nonempty absent change delivery")
			}
		case "CreditDraw":
			p := fields(m["params"])
			addNull(p["nullifiers"])
			cs := hashes(p["remainderCommitments"])
			ls := m["remainderLeafIndices"].([]uint32)
			vs := tuple[[]delivery](m["deliveries"])
			n := len(cs)
			if len(ls) != n || len(vs) != 2*n || len(hashes(p["nullifiers"])) != n || reflect.ValueOf(p["expiries"]).Len() != n || len(hashes(p["recoveryAmounts"])) != n {
				return model.Batch{}, fmt.Errorf("CreditDraw: array lengths differ")
			}
			id, x := tree(m["outputTreeId"])
			if x != nil {
				return model.Batch{}, x
			}
			for i, c := range cs {
				for j := 0; j < 2; j++ {
					v := vs[2*i+j]
					if c == (common.Hash{}) {
						if ls[i] != 0 || len(v.Ciphertext) != 0 || len(v.Clue) != 0 {
							return model.Batch{}, fmt.Errorf("CreditDraw: nonempty absent remainder")
						}
						continue
					}
					if err = add(id, ls[i], c, p["token"].(common.Address), v); err != nil {
						return model.Batch{}, err
					}
				}
			}
		}
	}
	return batch, nil
}

// Package protocol adapts the SDK-owned canonical Core receipt decoder.
package protocol

import (
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	sdkprotocol "github.com/invisi-labs/invisi-sdk-go/protocol"
	"github.com/invisi-labs/invisi-sdk-go/protocol/contracts"
	"github.com/invisi-labs/invisi-sdk-go/protocol/receipts"
)

type Decoder struct {
	abi    abi.ABI
	shared *receipts.Decoder
}

func NewDecoder(context ...sdkprotocol.TransactionContext) (*Decoder, error) {
	a, e := contracts.InvisiCoreMetaData.GetAbi()
	if e != nil {
		return nil, e
	}
	d, e := receipts.NewDecoder(context...)
	if e != nil {
		return nil, e
	}
	return &Decoder{abi: *a, shared: d}, nil
}
func (d *Decoder) Decode(block model.Block, logs []types.Log) (model.Batch, error) {
	b, e := d.shared.Decode(receipts.Block{Timestamp: block.Timestamp, Number: block.Number, Hash: block.Hash, ParentHash: block.ParentHash}, logs)
	if e != nil {
		return model.Batch{}, e
	}
	out := model.Batch{Block: block, Logs: b.Logs, CreatedTrees: b.CreatedTrees, Nullifiers: b.Nullifiers, CreditNullifiers: b.CreditNullifiers}
	for _, v := range b.Events {
		out.Events = append(out.Events, model.Event{Block: block, Name: v.Name, TransactionHash: v.TransactionHash, LogIndex: v.LogIndex, Data: v.Data})
	}
	for _, v := range b.Insertions {
		out.Insertions = append(out.Insertions, model.Insertion{TreeID: v.TreeID, Start: v.Start, Commitments: v.Commitments, LogIndex: v.LogIndex})
	}
	for _, v := range b.Outputs {
		out.Outputs = append(out.Outputs, model.NoteOutput{Block: block, TreeID: v.TreeID, LeafIndex: v.LeafIndex, Commitment: v.Commitment, Token: v.Token, Ciphertext: v.Ciphertext, Clue: v.Clue, TransactionHash: v.TransactionHash, LogIndex: v.LogIndex, Kind: v.Kind, OperationID: v.OperationID})
	}
	for _, v := range b.CreditStates {
		state := model.CreditState{Block: block, TreeID: v.TreeID, LeafIndex: v.LeafIndex, Commitment: v.Commitment, Token: v.Token, TransactionHash: v.TransactionHash, LogIndex: v.LogIndex, Kind: v.Kind}
		for _, delivery := range v.Deliveries {
			state.Deliveries = append(state.Deliveries, model.Delivery{Ciphertext: delivery.Ciphertext, Clue: delivery.Clue})
		}
		out.CreditStates = append(out.CreditStates, state)
	}
	return out, nil
}

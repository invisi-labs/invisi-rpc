package rpc

import (
	"context"
	"errors"
	"strconv"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
)

type typedBackend interface {
	CreditState(context.Context, common.Hash, uint64) (*model.CreditStateLookup, error)
	ConsumedAt(context.Context, common.Hash, uint64, bool) (bool, error)
	CreditAllocationAt(context.Context, common.Address, common.Address, uint64) (string, uint16, error)
	CreditBitmapAt(context.Context, common.Address, common.Address, string, uint8, uint64) (string, error)
}

func (a *API) typed() (typedBackend, error) {
	s, ok := a.Store.(typedBackend)
	if !ok {
		return nil, errors.New("typed index unavailable")
	}
	return s, nil
}
func (a *API) at(ctx context.Context, n uint64) (*model.Block, error) {
	b, e := a.Store.Block(ctx, n)
	if e != nil {
		return nil, e
	}
	if b == nil {
		return nil, errors.New("requested block is not indexed")
	}
	return b, nil
}
func (a *API) unchanged(ctx context.Context, b *model.Block) error {
	v, e := a.at(ctx, b.Number)
	if e != nil {
		return e
	}
	if v.Hash != b.Hash {
		return errors.New("query anchor reorganized; retry")
	}
	return nil
}
func (a *API) GetCreditState(ctx context.Context, c common.Hash, n uint64) (*model.CreditStateLookup, error) {
	if c == (common.Hash{}) {
		return nil, errors.New("nonzero commitment required")
	}
	var scalar fr.Element
	if e := scalar.SetBytesCanonical(c[:]); e != nil {
		return nil, errors.New("canonical commitment required")
	}
	s, e := a.typed()
	if e != nil {
		return nil, e
	}
	b, e := a.at(ctx, n)
	if e != nil {
		return nil, e
	}
	v, e := s.CreditState(ctx, c, n)
	if e != nil {
		return nil, e
	}
	return v, a.unchanged(ctx, b)
}

type NoteUpdates struct {
	BlockHash common.Hash        `json:"blockHash"`
	Notes     []model.NoteOutput `json:"notes"`
}

func (a *API) GetNoteUpdates(ctx context.Context, r Range) (*NoteUpdates, error) {
	if e := a.checkRange(ctx, r); e != nil {
		return nil, e
	}
	v, e := a.Store.Outputs(ctx, r.FromBlock, r.ToBlock, 10000)
	if e != nil {
		return nil, e
	}
	out := &NoteUpdates{r.BlockHash, v}
	if e = checkResponseSize(out); e != nil {
		return nil, e
	}
	return out, a.checkRange(ctx, r)
}
func (a *API) consumed(ctx context.Context, h common.Hash, n uint64, credit bool) (bool, error) {
	if h == (common.Hash{}) {
		return false, errors.New("nonzero nullifier required")
	}
	s, e := a.typed()
	if e != nil {
		return false, e
	}
	b, e := a.at(ctx, n)
	if e != nil {
		return false, e
	}
	v, e := s.ConsumedAt(ctx, h, n, credit)
	if e != nil {
		return false, e
	}
	return v, a.unchanged(ctx, b)
}
func (a *API) IsNoteSpent(ctx context.Context, h common.Hash, n uint64) (bool, error) {
	return a.consumed(ctx, h, n, false)
}
func (a *API) IsCreditConsumed(ctx context.Context, h common.Hash, n uint64) (bool, error) {
	return a.consumed(ctx, h, n, true)
}

type CreditAllocation struct {
	Block model.Block `json:"block"`
	Shard string      `json:"shard"`
	Slot  uint16      `json:"slot"`
}
type CreditBitmap struct {
	Block  model.Block `json:"block"`
	Bitmap string      `json:"bitmap"`
}

func (a *API) grantAnchor(ctx context.Context, owner, token common.Address, n uint64) (typedBackend, *model.Block, error) {
	if owner == (common.Address{}) || token == (common.Address{}) {
		return nil, nil, errors.New("nonzero owner and token required")
	}
	s, e := a.typed()
	if e != nil {
		return nil, nil, e
	}
	b, e := a.at(ctx, n)
	return s, b, e
}
func (a *API) GetCreditAllocation(ctx context.Context, owner, token common.Address, n uint64) (*CreditAllocation, error) {
	s, b, e := a.grantAnchor(ctx, owner, token, n)
	if e != nil {
		return nil, e
	}
	shard, slot, e := s.CreditAllocationAt(ctx, owner, token, n)
	if e != nil {
		return nil, e
	}
	return &CreditAllocation{*b, shard, slot}, a.unchanged(ctx, b)
}
func (a *API) GetCreditRevocationBitmap(ctx context.Context, owner, token common.Address, shard string, word uint8, n uint64) (*CreditBitmap, error) {
	sh, e := strconv.ParseUint(shard, 10, 64)
	if e != nil || sh == 0 || strconv.FormatUint(sh, 10) != shard {
		return nil, errors.New("canonical positive uint64 shard required")
	}
	s, b, e := a.grantAnchor(ctx, owner, token, n)
	if e != nil {
		return nil, e
	}
	bits, e := s.CreditBitmapAt(ctx, owner, token, shard, word, n)
	if e != nil {
		return nil, e
	}
	return &CreditBitmap{*b, bits}, a.unchanged(ctx, b)
}

type TransactionAvailability struct {
	TransactionHash common.Hash  `json:"transactionHash"`
	Status          string       `json:"status"`
	BlockNumber     *uint64      `json:"blockNumber,omitempty"`
	BlockHash       *common.Hash `json:"blockHash,omitempty"`
	ReceiptStatus   *uint64      `json:"receiptStatus,omitempty"`
	IndexedHead     *model.Block `json:"indexedHead"`
}
type TransactionNotes struct {
	TransactionAvailability
	Notes []model.NoteOutput `json:"notes"`
}
type TransactionCredits struct {
	TransactionAvailability
	States      []model.CreditState `json:"states"`
	Transitions []model.Event       `json:"transitions"`
}

func availability(v *TransactionUpdates) TransactionAvailability {
	return TransactionAvailability{v.TransactionHash, v.Status, v.BlockNumber, v.BlockHash, v.ReceiptStatus, v.IndexedHead}
}
func (a *API) GetTransactionNoteUpdates(ctx context.Context, h common.Hash) (*TransactionNotes, error) {
	v, e := a.transactionUpdates(ctx, h)
	if e != nil {
		return nil, e
	}
	return &TransactionNotes{availability(v), v.Outputs}, nil
}
func (a *API) GetTransactionCreditUpdates(ctx context.Context, h common.Hash) (*TransactionCredits, error) {
	v, e := a.transactionUpdates(ctx, h)
	if e != nil {
		return nil, e
	}
	out := &TransactionCredits{TransactionAvailability: availability(v)}
	if v.Status != "indexed" {
		return out, nil
	}
	out.States = []model.CreditState{}
	out.Transitions = v.CreditTransitions
	b, e := a.Store.Batch(ctx, *v.BlockNumber)
	if e != nil {
		return nil, e
	}
	if b == nil || b.Block.Hash != *v.BlockHash {
		return nil, errors.New("transaction anchor reorganized")
	}
	for _, s := range b.CreditStates {
		if s.TransactionHash == h {
			if s.Block != b.Block {
				return nil, errors.New("indexed Credit state block mismatch")
			}
			out.States = append(out.States, s)
		}
	}
	if e = checkResponseSize(out); e != nil {
		return nil, e
	}
	return out, a.unchanged(ctx, &b.Block)
}

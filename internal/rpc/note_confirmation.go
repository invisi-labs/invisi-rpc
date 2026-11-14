package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/big"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/cryptography"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/finality"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/indexer"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
)

// noteAPI changes only the JSON-RPC selector; the typed historic API is unchanged.
type noteAPI struct{ *API }

func (a *noteAPI) GetNote(ctx context.Context, commitment common.Hash, selector json.RawMessage) (any, error) {
	if len(selector) == 0 || bytes.Equal(selector, []byte("null")) {
		return nil, errors.New("note selector required")
	}
	if selector[0] != '{' {
		var height uint64
		if err := json.Unmarshal(selector, &height); err != nil {
			return nil, errors.New("expected indexed block number or finality selector")
		}
		return a.API.GetNote(ctx, commitment, height)
	}
	var selection struct {
		Finality string `json:"finality"`
	}
	decoder := json.NewDecoder(bytes.NewReader(selector))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&selection); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("invalid note selector")
	}
	if selection.Finality != "safe" && selection.Finality != "finalized" {
		return nil, errors.New("note confirmation requires safe or finalized")
	}
	return a.confirmNote(ctx, commitment, selection.Finality)
}

type noteConfirmation struct {
	Finality     string      `json:"finality"`
	VerifiedAt   model.Block `json:"verifiedAt"`
	FinalityHead model.Block `json:"finalityHead"`
}
type confirmedNote struct {
	VersionInfo
	Deployment   model.Deployment  `json:"deployment"`
	IndexedHead  *model.Block      `json:"indexedHead"`
	Status       string            `json:"status"`
	Reason       *string           `json:"reason"`
	Output       *model.NoteOutput `json:"output"`
	Membership   *model.Membership `json:"membership"`
	Confirmation *noteConfirmation `json:"confirmation"`
}

func selectedHead(s finality.Snapshot, tag string) finality.Head {
	if tag == "safe" {
		return s.Safe
	}
	return s.Finalized
}
func (a *API) canonicalNoteBlock(ctx context.Context, b model.Block) error {
	if a.Transactions == nil {
		return errors.New("note confirmation upstream is unavailable")
	}
	h, err := a.Transactions.HeaderByNumber(ctx, new(big.Int).SetUint64(b.Number))
	if err != nil {
		return err
	}
	if h == nil || h.Number == nil || !h.Number.IsUint64() || h.Number.Uint64() != b.Number || h.Hash() != b.Hash || h.Time != b.Timestamp || h.ParentHash != b.ParentHash {
		return errors.New("note confirmation block is no longer canonical; retry lookup")
	}
	return nil
}
func (a *API) confirmNote(ctx context.Context, commitment common.Hash, tag string) (*confirmedNote, error) {
	var scalar fr.Element
	if commitment == (common.Hash{}) {
		return nil, errors.New("nonzero commitment required")
	}
	if err := scalar.SetBytesCanonical(commitment[:]); err != nil {
		return nil, errors.New("canonical commitment required")
	}
	indexed, err := a.Store.Checkpoint(ctx)
	if err != nil {
		return nil, err
	}
	reason := "not_observed"
	result := &confirmedNote{VersionInfo: a.version, Deployment: a.Deployment, IndexedHead: indexed, Status: "pending", Reason: &reason}
	head := finality.Head{Status: "unavailable"}
	if a.Finality != nil {
		head = selectedHead(a.Finality.Read(ctx), tag)
	}
	if head.Status != "available" || head.Block == nil {
		switch head.Status {
		case "unsupported", "inconsistent":
			reason = "finality_" + head.Status
		default:
			reason = "finality_unavailable"
		}
		return result, nil
	}
	if indexed == nil {
		reason = "pending_index"
		return result, nil
	}
	at := *indexed
	if head.Block.Number < at.Number {
		at = *head.Block
	}
	if at.Number < a.Deployment.FromBlock {
		reason = "pending_index"
		return result, nil
	}
	// Authenticate both heads, including the indexed head, rather than trusting height alone.
	for _, b := range []model.Block{*indexed, *head.Block, at} {
		if err = a.canonicalNoteBlock(ctx, b); err != nil {
			return nil, err
		}
	}
	stored, err := a.Store.Block(ctx, at.Number)
	if err != nil {
		return nil, err
	}
	if stored == nil {
		reason = "pending_index"
		return result, nil
	}
	if *stored != at {
		return nil, errors.New("note confirmation index anchor mismatch")
	}
	lookup, err := a.GetNote(ctx, commitment, at.Number)
	if err != nil {
		return nil, err
	}
	if lookup == nil {
		// Distinguish an indexed, not-yet-confirmed output from an unobserved one.
		if indexed.Number > at.Number {
			future, e := a.GetNote(ctx, commitment, indexed.Number)
			if e != nil {
				return nil, e
			}
			if future != nil {
				reason = "confirmation_pending"
			}
		}
		return result, nil
	}
	output, proof := lookup.Output, lookup.Membership
	if output.Commitment != commitment || output.TransactionHash == (common.Hash{}) || output.Token == (common.Address{}) || output.TreeID == 0 || output.TreeID > 1<<53-1 || output.Block.Number < a.Deployment.FromBlock || output.Block.Number > at.Number || proof.Root.TreeID != output.TreeID || proof.LeafIndex != output.LeafIndex || proof.Commitment != commitment || proof.Root.LeafCount <= output.LeafIndex || proof.Root.LeafCount > 1<<cryptography.TreeDepth || proof.Root.Block.Number < output.Block.Number || proof.Root.Block.Number > at.Number || len(proof.Siblings) != cryptography.TreeDepth {
		return nil, errors.New("note confirmation output/proof binding mismatch")
	}
	for _, b := range []model.Block{output.Block, proof.Root.Block} {
		if err = a.canonicalNoteBlock(ctx, b); err != nil {
			return nil, err
		}
	}
	current := scalar
	for level, sibling := range proof.Siblings {
		var other fr.Element
		if err = other.SetBytesCanonical(sibling[:]); err != nil {
			return nil, errors.New("invalid note sibling field")
		}
		if proof.LeafIndex>>level&1 == 0 {
			current, err = cryptography.Poseidon2Hash(current, other)
		} else {
			current, err = cryptography.Poseidon2Hash(other, current)
		}
		if err != nil {
			return nil, err
		}
	}
	if common.Hash(current.Bytes()) != proof.Root.Root {
		return nil, errors.New("invalid note membership path")
	}
	receipt, err := a.Transactions.TransactionReceipt(ctx, output.TransactionHash)
	if err != nil {
		return nil, err
	}
	if receipt == nil || receipt.TxHash != output.TransactionHash || receipt.Status != 1 || receipt.BlockNumber == nil || !receipt.BlockNumber.IsUint64() || receipt.BlockNumber.Uint64() != output.Block.Number || receipt.BlockHash != output.Block.Hash {
		return nil, errors.New("note confirmation transaction receipt mismatch")
	}
	caller, ok := a.Transactions.(indexer.RootCaller)
	if !ok {
		return nil, errors.New("note confirmation root authentication unavailable")
	}
	root := proof.Root
	root.Block = at
	if err = indexer.RootValidator(caller, a.Deployment.Protocol)(ctx, root); err != nil {
		return nil, err
	}
	// Detect changes during upstream calls. A finality fence/unavailable head never confirms.
	after := selectedHead(a.Finality.Read(ctx), tag)
	if after.Status != "available" || after.Block == nil {
		reason = "finality_unavailable"
		if after.Status == "inconsistent" || after.Status == "unsupported" {
			reason = "finality_" + after.Status
		}
		return result, nil
	}
	if after.Block.Number < head.Block.Number {
		return nil, errors.New("native finality regressed during note confirmation")
	}
	for _, b := range []model.Block{output.Block, proof.Root.Block, at, *indexed, *head.Block, *after.Block} {
		if err = a.canonicalNoteBlock(ctx, b); err != nil {
			return nil, err
		}
	}
	stored, err = a.Store.Block(ctx, at.Number)
	if err != nil {
		return nil, err
	}
	if stored == nil || *stored != at {
		return nil, errors.New("note confirmation anchor reorganized")
	}
	result.Status = "confirmed"
	result.Reason = nil
	result.Output = &output
	result.Membership = &proof
	result.Confirmation = &noteConfirmation{Finality: tag, VerifiedAt: at, FinalityHead: *head.Block}
	return result, nil
}

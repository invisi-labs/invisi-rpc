package finality

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/invisi-labs/invisi-rpc/internal/model"
)

// Finalized performs the full normal finality/anchor checks. A nil block means
// no native finalized capability is available for the history fast path.
func (r *Reader) Finalized(ctx context.Context) (*model.Block, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out, e := r.read(ctx)
	if e != nil {
		return nil, e
	}
	if out.Latest.Status == "inconsistent" {
		return nil, fmt.Errorf("inconsistent native latest header")
	}
	if out.Latest.Status != "available" || out.Finalized.Status != "available" {
		return nil, nil
	}
	return out.Finalized.Block, nil
}

// ValidateAnchor is a read-only precommit guard. It intentionally takes neither
// Reader.mu nor store locks: API finality reads may be waiting for this writer.
// Persist an observed conflict with Fence only AFTER rolling back the transaction.
func (r *Reader) ValidateAnchor(ctx context.Context, anchor model.Block) error {
	if r.rangeFence.Load() {
		return ErrFinalityConflict
	}
	finalized := r.header(ctx, "finalized")
	if finalized.Block == nil {
		return fmt.Errorf("native finalized unavailable: %s", finalized.Status)
	}
	canonical := r.header(ctx, hexutil.EncodeUint64(anchor.Number))
	if canonical.Block == nil {
		return fmt.Errorf("accepted finalized anchor unavailable: %s", canonical.Status)
	}
	if !same(&anchor, canonical.Block) || (finalized.Block.Number == anchor.Number && !same(&anchor, finalized.Block)) {
		return ErrFinalityConflict
	}
	if finalized.Block.Number < anchor.Number {
		return fmt.Errorf("native finalized regressed behind accepted anchor")
	}
	if finalized.Block.Number > anchor.Number {
		current := r.header(ctx, hexutil.EncodeUint64(finalized.Block.Number))
		if current.Block == nil || !same(finalized.Block, current.Block) {
			return fmt.Errorf("native finalized header is not canonical")
		}
	}
	if r.rangeFence.Load() {
		return ErrFinalityConflict
	}
	return ctx.Err()
}
func (r *Reader) Fence(ctx context.Context) error {
	r.setRangeFence()
	if r.Store != nil {
		return r.Store.MarkFinalityConflict(ctx)
	}
	return nil
}

// AcquirePublication linearizes the final fence decision and database commit.
// The returned release must be held until commit or rollback completes. No network
// calls, Reader.mu acquisition, or store calls occur while acquiring this permit.
func (r *Reader) AcquirePublication(ctx context.Context) (func(), error) {
	r.publicationMu.Lock()
	if e := ctx.Err(); e != nil {
		r.publicationMu.Unlock()
		return nil, e
	}
	if r.rangeFence.Load() {
		r.publicationMu.Unlock()
		return nil, ErrFinalityConflict
	}
	return r.publicationMu.Unlock, nil
}
func (r *Reader) setRangeFence() {
	r.publicationMu.Lock()
	r.rangeFence.Store(true)
	r.publicationMu.Unlock()
}

// AcquireRollback also prevents removing an accepted finalized block when the
// indexer's network reads observe a reorg before an API read has fenced it.
func (r *Reader) AcquireRollback(ctx context.Context, ancestor uint64) (func(), error) {
	release, err := r.AcquirePublication(ctx)
	if err != nil {
		return nil, err
	}
	anchor := r.protectedAnchor
	if anchor == nil || ancestor >= anchor.Number {
		return release, nil
	}
	// A height bound alone does not prove a hash conflict: the provider may
	// temporarily lag behind an accepted anchor. Let the caller recheck finality
	// after releasing this permit, and fence only a verified history conflict.
	release()
	return nil, ErrFinalizedRollback
}

// ValidatePublication verifies the accepted finalized history without Reader.mu
// or store access. It is safe inside a writer transaction, before taking a permit.
func (r *Reader) ValidatePublication(ctx context.Context, block model.Block) error {
	r.publicationMu.Lock()
	var anchor *model.Block
	if r.protectedAnchor != nil {
		copy := *r.protectedAnchor
		anchor = &copy
	}
	r.publicationMu.Unlock()
	if anchor == nil {
		return nil
	}
	if block.Number == anchor.Number && !same(anchor, &block) {
		return ErrFinalityConflict
	}
	return r.ValidateAnchor(ctx, *anchor)
}

package finality

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/invisi-lab/invisi-rpc/internal/model"
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

// Package finality reads native execution RPC finality without a depth fallback.
package finality

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-lab/invisi-rpc/internal/model"
)

type Head struct {
	Status string       `json:"status"`
	Block  *model.Block `json:"block"`
}
type Snapshot struct {
	Latest    Head `json:"latest"`
	Safe      Head `json:"safe"`
	Finalized Head `json:"finalized"`
}

func Unavailable() Snapshot {
	return Snapshot{Latest: Head{Status: "unavailable"}, Safe: Head{Status: "unavailable"}, Finalized: Head{Status: "unavailable"}}
}

type RPC interface {
	CallContext(context.Context, any, string, ...any) error
}
type AnchorStore interface {
	FinalizedAnchor(context.Context) (*model.Block, error)
	SaveFinalizedAnchor(context.Context, model.Block) error
	FinalityConflict(context.Context) (bool, error)
	MarkFinalityConflict(context.Context) error
}
type Reader struct {
	RPC        RPC
	Store      AnchorStore
	mu         sync.Mutex
	conflicted bool
}

var ErrFinalityConflict = errors.New("accepted finalized history conflict; operator recovery required")

func (r *Reader) header(ctx context.Context, tag string) Head {
	var raw json.RawMessage
	if err := r.RPC.CallContext(ctx, &raw, "eth_getBlockByNumber", tag, false); err != nil {
		var coded interface{ ErrorCode() int }
		if errors.As(err, &coded) && (coded.ErrorCode() == -32601 || coded.ErrorCode() == -32602) {
			return Head{Status: "unsupported"}
		}
		return Head{Status: "unavailable"}
	}
	if len(raw) == 0 || string(raw) == "null" {
		return Head{Status: "unavailable"}
	}
	var h types.Header
	var declared struct {
		Hash      *common.Hash    `json:"hash"`
		Timestamp *hexutil.Uint64 `json:"timestamp"`
	}
	if json.Unmarshal(raw, &h) != nil || json.Unmarshal(raw, &declared) != nil || h.Number == nil || !h.Number.IsUint64() || declared.Timestamp == nil || declared.Hash == nil || *declared.Hash != h.Hash() {
		return Head{Status: "inconsistent"}
	}
	return Head{Status: "available", Block: &model.Block{Number: h.Number.Uint64(), Hash: h.Hash(), ParentHash: h.ParentHash, Timestamp: h.Time}}
}
func invalidate(h *Head) { h.Status = "inconsistent"; h.Block = nil }
func same(a, b *model.Block) bool {
	return a.Number == b.Number && a.Hash == b.Hash && a.Timestamp == b.Timestamp
}
func (r *Reader) sample(ctx context.Context) (Snapshot, Snapshot) {
	out := Unavailable()
	// Read the lower bounds first so ordinary advancing heads preserve ordering.
	out.Finalized = r.header(ctx, "finalized")
	out.Safe = r.header(ctx, "safe")
	out.Latest = r.header(ctx, "latest")
	observed := out
	heads := []*Head{&out.Finalized, &out.Safe, &out.Latest}
	for _, h := range heads {
		if h.Block != nil {
			canonical := r.header(ctx, hexutil.EncodeUint64(h.Block.Number))
			if canonical.Block == nil {
				if canonical.Status == "inconsistent" {
					invalidate(h)
				} else {
					h.Status = "unavailable"
					h.Block = nil
				}
			} else if !same(h.Block, canonical.Block) {
				invalidate(h)
			}
		}
	}
	for a := 0; a < len(heads); a++ {
		for b := a + 1; b < len(heads); b++ {
			x, y := heads[a], heads[b]
			if x.Block != nil && y.Block != nil && (x.Block.Number > y.Block.Number || (x.Block.Number == y.Block.Number && x.Block.Hash != y.Block.Hash)) {
				invalidate(x)
				invalidate(y)
			}
		}
	}
	return out, observed
}
func (r *Reader) read(ctx context.Context) (Snapshot, error) {
	out, observed := r.sample(ctx)
	observedFinalized := observed.Finalized.Block
	observedLatest := observed.Latest.Block
	// A concurrent reorg can invalidate an otherwise correct sample; retry once.
	if out.Latest.Status == "inconsistent" || out.Safe.Status == "inconsistent" || out.Finalized.Status == "inconsistent" {
		retry, observedRetry := r.sample(ctx)
		out = retry
		if observedFinalized == nil {
			observedFinalized = observedRetry.Finalized.Block
		}
	}
	if r.Store == nil {
		return out, nil
	}
	fenced, fenceErr := r.Store.FinalityConflict(ctx)
	if fenceErr != nil {
		out.Finalized = Head{Status: "unavailable"}
		return out, fenceErr
	}
	if fenced || r.conflicted {
		invalidate(&out.Finalized)
		return out, ErrFinalityConflict
	}
	anchor, err := r.Store.FinalizedAnchor(ctx)
	if err != nil {
		out.Finalized = Head{Status: "unavailable"}
		return out, err
	}
	if anchor != nil {
		canonical := r.header(ctx, hexutil.EncodeUint64(anchor.Number))
		if canonical.Block == nil {
			out.Finalized = Head{Status: canonical.Status}
			return out, fmt.Errorf("cannot verify accepted finalized anchor: %s", canonical.Status)
		}
		if canonical.Block.Number != anchor.Number {
			out.Finalized = Head{Status: "inconsistent"}
			return out, errors.New("RPC returned wrong finalized anchor height")
		}
		if (observedFinalized != nil && observedFinalized.Number == anchor.Number && !same(anchor, observedFinalized)) || !same(anchor, canonical.Block) {
			invalidate(&out.Finalized)
			r.conflicted = true
			if err := r.Store.MarkFinalityConflict(ctx); err != nil {
				return out, fmt.Errorf("%w: persist fence: %v", ErrFinalityConflict, err)
			}
			return out, ErrFinalityConflict
		}
		if (observedFinalized != nil && observedFinalized.Number < anchor.Number) || (observedLatest != nil && observedLatest.Number < anchor.Number) {
			out.Finalized = Head{Status: "unavailable"}
			return out, errors.New("upstream finality is behind accepted anchor")
		}
	}
	if out.Finalized.Block != nil && (anchor == nil || !same(anchor, out.Finalized.Block)) {
		if err = r.Store.SaveFinalizedAnchor(ctx, *out.Finalized.Block); err != nil {
			out.Finalized = Head{Status: "unavailable"}
			return out, err
		}
	}
	return out, nil
}
func (r *Reader) Read(ctx context.Context) Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	out, _ := r.read(ctx)
	return out
}

// Check prevents indexing from rolling back history accepted as finalized, even after restart.
func (r *Reader) Check(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	out, err := r.read(ctx)
	if err == nil && out.Latest.Status == "inconsistent" {
		return errors.New("inconsistent native latest header")
	}
	return err
}

package indexer

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-rpc/internal/finality"
	"github.com/invisi-labs/invisi-rpc/internal/model"
)

type headerRange interface {
	Headers(context.Context, uint64, uint64) ([]*types.Header, error)
}
type rangeStoreWriter interface {
	ApplyRange(context.Context, []model.Batch, func(context.Context) (func(), error)) error
}

// Finalized must perform the same persistent anchor/fence checks as Check.
// ValidateAnchor must be store/mutex independent for use inside a write transaction.
type rangeFinalityReader interface {
	Finalized(context.Context) (*model.Block, error)
	ValidateAnchor(context.Context, model.Block) error
	Fence(context.Context) error
	AcquirePublication(context.Context) (func(), error)
}
type rangeAttempt struct{ handled, progress, finalityChecked bool }

func (i *Indexer) stepRange(ctx context.Context) (attempt rangeAttempt, err error) {
	if i.BatchSize > 128 {
		return attempt, errors.New("index batch size exceeds 128")
	}
	chain, ok := i.Chain.(headerRange)
	if !ok {
		return attempt, nil
	}
	store, ok := i.Store.(rangeStoreWriter)
	if !ok {
		return attempt, nil
	}
	fin, ok := i.Finality.(rangeFinalityReader)
	if !ok || time.Now().Before(i.batchRetryAfter) {
		return attempt, nil
	}
	anchor, e := fin.Finalized(ctx)
	if e != nil {
		return attempt, e
	}
	attempt.finalityChecked = true
	if anchor == nil {
		return attempt, nil
	}
	startAnchor := *anchor
	cp, e := i.Store.Checkpoint(ctx)
	if e != nil {
		return attempt, e
	}
	next := i.Deployment.IndexFromBlock()
	if cp != nil {
		if cp.Number >= startAnchor.Number {
			return attempt, nil
		}
		next = cp.Number + 1
	}
	limit := i.BatchSize
	if i.batchLimit > 1 && i.batchLimit < limit {
		limit = i.batchLimit
	}
	if next > startAnchor.Number || startAnchor.Number-next+1 < uint64(limit) {
		return attempt, nil
	}
	if cp != nil {
		h, e := i.Chain.HeaderByNumber(ctx, new(big.Int).SetUint64(cp.Number))
		if e != nil {
			return attempt, e
		}
		if h == nil || h.Number == nil || !h.Number.IsUint64() || h.Number.Uint64() != cp.Number {
			return attempt, errors.New("invalid checkpoint header")
		}
		if h.Hash() != cp.Hash {
			return attempt, nil
		}
		if h.Time != cp.Timestamp {
			return attempt, errors.New("saved checkpoint timestamp differs from canonical header")
		}
	}
	headers, e := chain.Headers(ctx, next, next+uint64(limit)-1)
	if errors.Is(e, ErrBatchUnavailable) {
		i.batchRetryAfter = time.Now().Add(time.Minute)
		return attempt, nil
	}
	if e != nil {
		return attempt, e
	}
	if len(headers) != limit {
		return attempt, errors.New("incomplete header range")
	}
	parent := common.Hash{}
	if cp != nil {
		parent = cp.Hash
	}
	for n, h := range headers {
		number := next + uint64(n)
		if h == nil || h.Number == nil || !h.Number.IsUint64() || h.Number.Uint64() != number || h.Number.BitLen() > 63 {
			return attempt, errors.New("invalid range header height")
		}
		if (n > 0 || cp != nil) && h.ParentHash != parent {
			return attempt, errors.New("range header parent mismatch")
		}
		parent = h.Hash()
	}
	addresses := []common.Address{i.Deployment.Protocol}
	if i.Deployment.Registry != nil {
		addresses = append(addresses, i.Deployment.Registry.Address)
	}
	if i.Deployment.BroadcasterRegistry != nil {
		addresses = append(addresses, i.Deployment.BroadcasterRegistry.Address)
	}
	var logs []types.Log
	for {
		logs, e = i.Chain.FilterLogs(ctx, ethereum.FilterQuery{FromBlock: new(big.Int).SetUint64(next), ToBlock: new(big.Int).SetUint64(next + uint64(limit) - 1), Addresses: addresses})
		if e == nil {
			break
		}
		if ctx.Err() != nil {
			return attempt, ctx.Err()
		}
		limit /= 2
		if limit < 2 {
			i.batchRetryAfter = time.Now().Add(time.Minute)
			return attempt, nil
		}
	}
	i.batchLimit = limit
	perBlock := make([][]types.Log, limit)
	seen := make([]map[uint]bool, limit)
	for _, l := range logs {
		if l.BlockNumber < next || l.BlockNumber-next >= uint64(limit) || !allowedEmitter(i.Deployment, l.Address, l.BlockNumber) || l.Removed {
			return attempt, errors.New("RPC returned out-of-scope range log")
		}
		pos := int(l.BlockNumber - next)
		if l.BlockHash != headers[pos].Hash() {
			return attempt, errors.New("RPC returned inconsistent range log hash")
		}
		if seen[pos] == nil {
			seen[pos] = map[uint]bool{}
		}
		if seen[pos][l.Index] {
			return attempt, errors.New("RPC returned duplicate range log index")
		}
		seen[pos][l.Index] = true
		perBlock[pos] = append(perBlock[pos], l)
	}
	blocks := make([]model.Batch, limit)
	for n := range blocks {
		h := headers[n]
		b := model.Block{Number: next + uint64(n), Hash: h.Hash(), ParentHash: h.ParentHash, Timestamp: h.Time}
		blocks[n], e = i.Decoder.Decode(b, perBlock[n])
		if e != nil {
			return attempt, fmt.Errorf("decode range block %d: %w", b.Number, e)
		}
		if blocks[n].Block != b {
			return attempt, errors.New("decoder changed range block identity")
		}
		blocks[n].Header = types.CopyHeader(h)
	}
	// Recheck the full persistent finality context after fetching, before taking a writer lock.
	anchor, e = fin.Finalized(ctx)
	if e != nil {
		return attempt, e
	}
	last := blocks[len(blocks)-1].Block
	if anchor == nil || anchor.Number < startAnchor.Number || anchor.Number < last.Number {
		return attempt, errors.New("finalized history unavailable during range validation")
	}
	accepted := *anchor
	guard := func(guardCtx context.Context) (func(), error) {
		h, terminalErr := i.Chain.HeaderByNumber(guardCtx, new(big.Int).SetUint64(last.Number))
		if terminalErr == nil && (h == nil || h.Number == nil || !h.Number.IsUint64() || h.Number.Uint64() != last.Number || h.Hash() != last.Hash || h.Time != last.Timestamp) {
			terminalErr = errors.New("chain changed during range commit")
			// Retain a directly observed accepted-anchor conflict even if a subsequent
			// provider response recovers before ValidateAnchor's numeric lookup.
			if h != nil && h.Number != nil && h.Number.IsUint64() && h.Number.Uint64() == accepted.Number && (h.Hash() != accepted.Hash || h.Time != accepted.Timestamp) {
				terminalErr = finality.ErrFinalityConflict
			}
		}
		finalityErr := fin.ValidateAnchor(guardCtx, accepted)
		if errors.Is(terminalErr, finality.ErrFinalityConflict) {
			return nil, terminalErr
		}
		if finalityErr != nil {
			return nil, finalityErr
		}
		if terminalErr != nil {
			return nil, terminalErr
		}
		return fin.AcquirePublication(guardCtx)
	}

	e = store.ApplyRange(ctx, blocks, guard)
	if errors.Is(e, finality.ErrFinalityConflict) {
		fenceCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if persistErr := fin.Fence(fenceCtx); persistErr != nil {
			return attempt, fmt.Errorf("%w: persist range conflict: %v", e, persistErr)
		}
	}
	if e != nil {
		return attempt, e
	}
	attempt.handled = true
	attempt.progress = true
	return attempt, nil
}

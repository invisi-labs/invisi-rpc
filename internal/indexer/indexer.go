// Package indexer follows a single protocol deployment on its canonical chain.
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
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/invisi-labs/invisi-rpc/internal/finality"
	"github.com/invisi-labs/invisi-rpc/internal/model"
)

type Chain interface {
	ChainID(context.Context) (*big.Int, error)
	HeaderByNumber(context.Context, *big.Int) (*types.Header, error)
	CodeAt(context.Context, common.Address, *big.Int) ([]byte, error)
	FilterLogs(context.Context, ethereum.FilterQuery) ([]types.Log, error)
}
type Store interface {
	Checkpoint(context.Context) (*model.Block, error)
	Block(context.Context, uint64) (*model.Block, error)
	Apply(context.Context, model.Batch) error
	Rollback(context.Context, uint64) error
}
type Decoder interface {
	Decode(model.Block, []types.Log) (model.Batch, error)
}
type Indexer struct {
	BatchSize       int
	batchLimit      int
	batchRetryAfter time.Time
	Chain           Chain
	Store           Store
	Decoder         Decoder
	Deployment      model.Deployment
	Finality        interface{ Check(context.Context) error }
}

func (i *Indexer) Verify(ctx context.Context) error {
	id, err := i.Chain.ChainID(ctx)
	if err != nil {
		return err
	}
	if id.String() != i.Deployment.ChainID {
		return fmt.Errorf("chain ID mismatch: got %s", id)
	}
	code, err := i.Chain.CodeAt(ctx, i.Deployment.Protocol, nil)
	if err != nil {
		return err
	}
	if len(code) == 0 || crypto.Keccak256Hash(code) != i.Deployment.RuntimeCodeHash {
		return errors.New("protocol runtime code hash mismatch")
	}
	if i.Deployment.Registry != nil {
		code, err = i.Chain.CodeAt(ctx, i.Deployment.Registry.Address, nil)
		if err != nil {
			return err
		}
		if len(code) == 0 || crypto.Keccak256Hash(code) != i.Deployment.Registry.RuntimeCodeHash {
			return errors.New("registry runtime code hash mismatch")
		}
	}
	if i.Deployment.BroadcasterRegistry != nil {
		code, err = i.Chain.CodeAt(ctx, i.Deployment.BroadcasterRegistry.Address, nil)
		if err != nil {
			return err
		}
		if len(code) == 0 || crypto.Keccak256Hash(code) != i.Deployment.BroadcasterRegistry.RuntimeCodeHash {
			return errors.New("broadcaster registry runtime code hash mismatch")
		}
	}
	return nil
}
func allowedEmitter(d model.Deployment, address common.Address, number uint64) bool {
	return (d.BroadcasterRegistry != nil && address == d.BroadcasterRegistry.Address && number >= d.BroadcasterRegistry.FromBlock) || (address == d.Protocol && number >= d.FromBlock) || (d.Registry != nil && address == d.Registry.Address && number >= d.Registry.FromBlock)
}

// Step processes a bounded finalized history range or one canonical head block.
// A false result means the indexer is caught up.
func (i *Indexer) Step(ctx context.Context) (bool, error) {
	if i.BatchSize > 1 {
		attempt, e := i.stepRange(ctx)
		if e != nil || attempt.handled {
			return attempt.progress, e
		}
		return i.step(ctx, attempt.finalityChecked)
	}
	return i.step(ctx, false)
}
func (i *Indexer) step(ctx context.Context, finalityChecked bool) (bool, error) {
	if i.Finality != nil && !finalityChecked {
		if err := i.Finality.Check(ctx); err != nil {
			return false, err
		}
	}
	head, err := i.Chain.HeaderByNumber(ctx, nil)
	if err != nil {
		return false, err
	}
	if head == nil || head.Number == nil || !head.Number.IsUint64() {
		return false, errors.New("RPC returned invalid latest header")
	}
	cp, err := i.Store.Checkpoint(ctx)
	if err != nil {
		return false, err
	}
	// Reconcile even when no new blocks are available.
	for cp != nil {
		if cp.Number > head.Number.Uint64() {
			if cp.Number <= i.Deployment.IndexFromBlock() {
				return false, errors.New("chain head precedes deployment; verify deployment before reset")
			}
			if err = i.rollback(ctx, cp.Number-1); err != nil {
				return false, err
			}
			cp, err = i.Store.Checkpoint(ctx)
			if err != nil {
				return false, err
			}
			continue
		}
		h, e := i.Chain.HeaderByNumber(ctx, new(big.Int).SetUint64(cp.Number))
		if e != nil {
			return false, e
		}
		if h.Hash() == cp.Hash {
			if h.Time != cp.Timestamp {
				return false, errors.New("saved checkpoint timestamp differs from canonical header")
			}
			break
		}
		if cp.Number == i.Deployment.IndexFromBlock() {
			return false, errors.New("deployment starting block was reorganized; reset database after verifying deployment")
		}
		ancestor := cp.Number - 1
		if err = i.rollback(ctx, ancestor); err != nil {
			return false, err
		}
		cp, err = i.Store.Checkpoint(ctx)
		if err != nil {
			return false, err
		}
	}
	next := i.Deployment.IndexFromBlock()
	if cp != nil {
		next = cp.Number + 1
	}
	if next > head.Number.Uint64() {
		return false, nil
	}
	h, err := i.Chain.HeaderByNumber(ctx, new(big.Int).SetUint64(next))
	if err != nil {
		return false, err
	}
	if h.Number == nil || !h.Number.IsUint64() || h.Number.Uint64() != next {
		return false, errors.New("RPC returned inconsistent block header")
	}
	if cp != nil && h.ParentHash != cp.Hash {
		return false, errors.New("chain changed during indexing; retry")
	}
	hash := h.Hash()
	addresses := []common.Address{}
	if next >= i.Deployment.FromBlock {
		addresses = append(addresses, i.Deployment.Protocol)
	}
	if i.Deployment.Registry != nil && next >= i.Deployment.Registry.FromBlock {
		addresses = append(addresses, i.Deployment.Registry.Address)
	}
	if i.Deployment.BroadcasterRegistry != nil && next >= i.Deployment.BroadcasterRegistry.FromBlock {
		addresses = append(addresses, i.Deployment.BroadcasterRegistry.Address)
	}
	logs, err := i.Chain.FilterLogs(ctx, ethereum.FilterQuery{BlockHash: &hash, Addresses: addresses})
	if err != nil {
		return false, err
	}
	seenLogs := make(map[uint]struct{}, len(logs))
	for _, l := range logs {
		if _, ok := seenLogs[l.Index]; ok {
			return false, errors.New("RPC returned duplicate log index")
		}
		seenLogs[l.Index] = struct{}{}
		if l.BlockHash != hash || l.BlockNumber != next || !allowedEmitter(i.Deployment, l.Address, next) || l.Removed {
			return false, errors.New("RPC returned inconsistent log")
		}
	}
	b, err := i.Decoder.Decode(model.Block{Number: next, Hash: hash, ParentHash: h.ParentHash, Timestamp: h.Time}, logs)
	if err != nil {
		return false, fmt.Errorf("decode block %d: %w", next, err)
	}
	if b.Block != (model.Block{Number: next, Hash: hash, ParentHash: h.ParentHash, Timestamp: h.Time}) {
		return false, errors.New("decoder changed block identity")
	}
	b.Header = types.CopyHeader(h)
	// Catch a concurrent reorg before publishing the block. Root validation uses the block hash too.
	current, err := i.Chain.HeaderByNumber(ctx, new(big.Int).SetUint64(next))
	if err != nil {
		return false, err
	}
	if current.Hash() != hash {
		return false, errors.New("chain changed during indexing; retry")
	}
	if err = i.apply(ctx, b); err != nil {
		return false, fmt.Errorf("apply block %d: %w", next, err)
	}
	return true, nil
}

// publish holds the finality permit only across a store mutation. Network reads
// and Check must remain outside it: a concurrent API read may persist a fence.
func (i *Indexer) publish(ctx context.Context, write func() error) error {
	if fin, ok := i.Finality.(interface {
		AcquirePublication(context.Context) (func(), error)
	}); ok {
		release, err := fin.AcquirePublication(ctx)
		if err != nil {
			return err
		}
		defer release()
	}
	return write()
}

func (i *Indexer) apply(ctx context.Context, b model.Batch) error {
	fin, ok := i.Finality.(interface {
		AcquirePublication(context.Context) (func(), error)
	})
	if !ok {
		return i.Store.Apply(ctx, b)
	}
	guard := func(ctx context.Context) (func(), error) {
		if validator, ok := i.Finality.(interface {
			ValidatePublication(context.Context, model.Block) error
		}); ok {
			if err := validator.ValidatePublication(ctx, b.Block); err != nil {
				return nil, err
			}
		}
		return fin.AcquirePublication(ctx)
	}
	var err error
	if store, ok := i.Store.(rangeStoreWriter); ok {
		// Root validation and anchor network checks run before the publication permit.
		err = store.ApplyRange(ctx, []model.Batch{b}, guard)
	} else {
		var release func()
		release, err = guard(ctx)
		if err == nil {
			err = i.Store.Apply(ctx, b)
			release()
		}
	}
	// A transaction must finish rolling back before Fence persists through the
	// store writer. Preserve directly observed conflicts even if the provider recovers.
	if errors.Is(err, finality.ErrFinalityConflict) {
		if fence, ok := i.Finality.(interface{ Fence(context.Context) error }); ok {
			fenceCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if persistErr := fence.Fence(fenceCtx); persistErr != nil {
				return fmt.Errorf("%w: persist publication conflict: %v", err, persistErr)
			}
		}
	}
	return err
}

func (i *Indexer) rollback(ctx context.Context, ancestor uint64) error {
	if fin, ok := i.Finality.(interface {
		AcquireRollback(context.Context, uint64) (func(), error)
	}); ok {
		guard := func(ctx context.Context) (func(), error) { return fin.AcquireRollback(ctx, ancestor) }
		var err error
		if store, ok := i.Store.(interface {
			RollbackGuarded(context.Context, uint64, func(context.Context) (func(), error)) error
		}); ok {
			err = store.RollbackGuarded(ctx, ancestor, guard)
		} else {
			var release func()
			release, err = guard(ctx)
			if err == nil {
				err = i.Store.Rollback(ctx, ancestor)
				release()
			}
		}
		if errors.Is(err, finality.ErrFinalizedRollback) {
			// The writer transaction and permit are released before any network reads.
			if checkErr := i.Finality.Check(ctx); checkErr != nil {
				return checkErr
			}
		}
		return err
	}
	return i.publish(ctx, func() error { return i.Store.Rollback(ctx, ancestor) })
}

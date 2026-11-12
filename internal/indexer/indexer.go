// Package indexer follows a single protocol deployment on its canonical chain.
package indexer

import (
	"context"
	"errors"
	"fmt"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
	"math/big"
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
	Chain      Chain
	Store      Store
	Decoder    Decoder
	Deployment model.Deployment
	Finality   interface{ Check(context.Context) error }
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

// Step processes at most one canonical block. A false result means the indexer is caught up.
func (i *Indexer) Step(ctx context.Context) (bool, error) {
	if i.Finality != nil {
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
			if err = i.Store.Rollback(ctx, cp.Number-1); err != nil {
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
		if err = i.Store.Rollback(ctx, ancestor); err != nil {
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
	// Catch a concurrent reorg before publishing the block. Root validation uses the block hash too.
	current, err := i.Chain.HeaderByNumber(ctx, new(big.Int).SetUint64(next))
	if err != nil {
		return false, err
	}
	if current.Hash() != hash {
		return false, errors.New("chain changed during indexing; retry")
	}
	if err = i.Store.Apply(ctx, b); err != nil {
		return false, fmt.Errorf("apply block %d: %w", next, err)
	}
	return true, nil
}

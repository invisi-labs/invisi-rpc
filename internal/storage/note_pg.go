package storage

import (
	"context"
	"encoding/json"
	"errors"
	"math"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	"github.com/jackc/pgx/v5"
)

// Note looks up a leaf through an index, then reads its output and proof in one snapshot.
func (s *Store) Note(ctx context.Context, commitment common.Hash, atBlock uint64) (*model.NoteLookup, error) {
	return s.lookupLeaf(ctx, commitment, atBlock)
}

func (s *Store) lookupLeaf(ctx context.Context, commitment common.Hash, atBlock uint64) (*model.NoteLookup, error) {
	if atBlock > math.MaxInt64 {
		return nil, errors.New("block number out of range")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var tree uint64
	var index uint32
	var raw []byte
	err = tx.QueryRow(ctx, "SELECT n.tree_id,n.node_index,b.batch FROM invisi_nodes n JOIN invisi_blocks b ON b.deployment=n.deployment AND b.number=n.block_number WHERE n.deployment=$1 AND n.level=0 AND n.hash=$2 AND n.block_number<=$3 ORDER BY n.block_number DESC LIMIT 1", s.key, commitment[:], atBlock).Scan(&tree, &index, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var batch model.Batch
	if err = json.Unmarshal(raw, &batch); err != nil {
		return nil, err
	}
	var output *model.NoteOutput
	values := batch.Outputs
	for _, value := range values {
		if value.Commitment == commitment && value.TreeID == tree && value.LeafIndex == index {
			copy := value
			output = &copy
			break
		}
	}
	if output == nil {
		return nil, nil
	}
	if output.Block != batch.Block {
		return nil, errors.New("indexed output block mismatch")
	}
	var rootBytes []byte
	err = tx.QueryRow(ctx, "SELECT root FROM invisi_roots WHERE deployment=$1 AND tree_id=$2 AND block_number<=$3 AND leaf_count>$4 ORDER BY leaf_count DESC LIMIT 1", s.key, tree, atBlock, index).Scan(&rootBytes)
	if err != nil {
		return nil, err
	}
	membership, err := s.membership(ctx, tx, tree, index, common.BytesToHash(rootBytes))
	if err != nil {
		return nil, err
	}
	if membership.Commitment != commitment {
		return nil, errors.New("indexed membership commitment mismatch")
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &model.NoteLookup{Output: *output, Membership: *membership}, nil
}

func (s *Store) CreditState(ctx context.Context, commitment common.Hash, atBlock uint64) (*model.CreditStateLookup, error) {
	if atBlock > math.MaxInt64 {
		return nil, errors.New("block number out of range")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var tree uint64
	var index uint32
	var raw []byte
	err = tx.QueryRow(ctx, "SELECT n.tree_id,n.node_index,b.batch FROM invisi_nodes n JOIN invisi_blocks b ON b.deployment=n.deployment AND b.number=n.block_number WHERE n.deployment=$1 AND n.level=0 AND n.hash=$2 AND n.block_number<=$3 ORDER BY n.block_number DESC LIMIT 1", s.key, commitment[:], atBlock).Scan(&tree, &index, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var batch model.Batch
	if err = json.Unmarshal(raw, &batch); err != nil {
		return nil, err
	}
	var output *model.CreditState
	values := batch.CreditStates
	for _, value := range values {
		if value.Commitment == commitment && value.TreeID == tree && value.LeafIndex == index {
			copy := value
			output = &copy
			break
		}
	}
	if output == nil {
		return nil, nil
	}
	if output.Block != batch.Block {
		return nil, errors.New("indexed output block mismatch")
	}
	var rootBytes []byte
	err = tx.QueryRow(ctx, "SELECT root FROM invisi_roots WHERE deployment=$1 AND tree_id=$2 AND block_number<=$3 AND leaf_count>$4 ORDER BY leaf_count DESC LIMIT 1", s.key, tree, atBlock, index).Scan(&rootBytes)
	if err != nil {
		return nil, err
	}
	membership, err := s.membership(ctx, tx, tree, index, common.BytesToHash(rootBytes))
	if err != nil {
		return nil, err
	}
	if membership.Commitment != commitment {
		return nil, errors.New("indexed membership commitment mismatch")
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &model.CreditStateLookup{State: *output, Membership: *membership}, nil
}

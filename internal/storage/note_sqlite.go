package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
	"math"
)

// Note looks up a leaf through an index, then reads its output and proof in one snapshot.
func (s *SQLiteStore) Note(ctx context.Context, commitment common.Hash, atBlock uint64) (*model.NoteLookup, error) {
	if atBlock > math.MaxInt64 {
		return nil, errors.New("block number out of range")
	}
	tx, err := s.readPool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var tree uint64
	var index uint32
	var raw []byte
	err = tx.QueryRow(ctx, "SELECT n.tree_id,n.node_index,b.batch FROM invisi_nodes n JOIN invisi_blocks b ON b.deployment=n.deployment AND b.number=n.block_number WHERE n.deployment=?1 AND n.level=0 AND n.hash=?2 AND n.block_number<=?3 ORDER BY n.block_number DESC LIMIT 1", s.key, commitment[:], atBlock).Scan(&tree, &index, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var batch model.Batch
	if err = json.Unmarshal(raw, &batch); err != nil {
		return nil, err
	}
	var output *model.Output
	for _, value := range batch.Outputs {
		if value.Commitment == commitment && value.TreeID == tree && value.LeafIndex == index {
			copy := value
			output = &copy
			break
		}
	}
	if output == nil {
		return nil, errors.New("indexed commitment has no matching output")
	}
	if output.Block != batch.Block {
		return nil, errors.New("indexed output block mismatch")
	}
	var rootBytes []byte
	err = tx.QueryRow(ctx, "SELECT root FROM invisi_roots WHERE deployment=?1 AND tree_id=?2 AND block_number<=?3 AND leaf_count>?4 ORDER BY leaf_count DESC LIMIT 1", s.key, tree, atBlock, index).Scan(&rootBytes)
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

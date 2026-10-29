package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
)

// CurrentRoots returns one latest root per tree from a single database statement.
// Startup validation can check restored derived state without loading root history.
func CurrentRoots(ctx context.Context, backend Backend) ([]model.Root, error) {
	return currentRoots(ctx, backend, math.MaxInt64)
}

// CurrentRootsAt selects roots recorded by height, excluding later tree creation
// and inserts committed by concurrent indexers after a startup checkpoint read.
func CurrentRootsAt(ctx context.Context, backend Backend, height uint64) ([]model.Root, error) {
	if height > math.MaxInt64 {
		return nil, errors.New("block height out of range")
	}
	return currentRoots(ctx, backend, height)
}

func currentRoots(ctx context.Context, backend Backend, height uint64) ([]model.Root, error) {
	const query = `SELECT r.tree_id,r.leaf_count,r.root,b.number,b.hash,b.parent_hash,b.batch->'block'
 FROM invisi_roots r JOIN invisi_blocks b ON b.deployment=r.deployment AND b.number=r.block_number
 WHERE r.deployment=$1 AND r.block_number<=$2 AND NOT EXISTS
 (SELECT 1 FROM invisi_roots newer WHERE newer.deployment=r.deployment AND newer.tree_id=r.tree_id AND newer.leaf_count>r.leaf_count AND newer.block_number<=$2)
 ORDER BY r.tree_id`
	var rows rootRows
	switch s := backend.(type) {
	case *Store:
		if s == nil {
			return nil, errors.New("nil PostgreSQL backend")
		}
		r, err := s.pool.Query(ctx, query, s.key, height)
		if err != nil {
			return nil, err
		}
		rows = r
	case *SQLiteStore:
		if s == nil {
			return nil, errors.New("nil SQLite backend")
		}
		r, err := s.readPool.Query(ctx, strings.NewReplacer("$1", "?1", "$2", "?2").Replace(query), s.key, height)
		if err != nil {
			return nil, err
		}
		rows = sqliteRootRows{r}
	default:
		return nil, errors.New("current roots requires a database backend")
	}
	defer rows.Close()
	roots := []model.Root{}
	for rows.Next() {
		var r model.Root
		var hash, blockHash, parent, blockJSON []byte
		if err := rows.Scan(&r.TreeID, &r.LeafCount, &hash, &r.Block.Number, &blockHash, &parent, &blockJSON); err != nil {
			return nil, err
		}
		r.Root = common.BytesToHash(hash)
		r.Block.Hash = common.BytesToHash(blockHash)
		r.Block.ParentHash = common.BytesToHash(parent)
		if err := json.Unmarshal(blockJSON, &r.Block); err != nil {
			return nil, err
		}
		roots = append(roots, r)
	}
	return roots, rows.Err()
}

type rootRows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}
type sqliteRootRows struct{ *sql.Rows }

func (r sqliteRootRows) Close() { _ = r.Rows.Close() }

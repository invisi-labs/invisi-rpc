package storage

import (
	"context"
	"errors"

	"github.com/invisi-labs/invisi-rpc/internal/model"
)

const MaxApplyRange = 128

// ApplyRange publishes a bounded sequence atomically, retaining each block's
// continuity, event, Merkle and root checks. guard must not call this store: it
// runs with the writer transaction held, immediately before the final fence check.
// Its optional publication release is held through commit/rollback.
func (s *SQLiteStore) ApplyRange(ctx context.Context, blocks []model.Batch, guard func(context.Context) (func(), error)) error {
	return s.ApplyRangeWithFinality(ctx, blocks, func(ctx context.Context, _ *model.Block) (func(), error) {
		if guard == nil {
			return nil, nil
		}
		return guard(ctx)
	})
}

// ApplyRangeWithFinality binds the commit guard to this writer's durable anchor.
func (s *SQLiteStore) ApplyRangeWithFinality(ctx context.Context, blocks []model.Batch, guard func(context.Context, *model.Block) (func(), error)) error {
	if len(blocks) == 0 || len(blocks) > MaxApplyRange {
		return errors.New("invalid block range size")
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	var release func()
	defer func() {
		_ = tx.Rollback(ctx)
		if release != nil {
			release()
		}
	}()
	for _, b := range blocks {
		if e = s.apply(ctx, tx, b); e != nil {
			return e
		}
	}
	var raw []byte
	var conflicted int
	e = tx.QueryRow(ctx, "SELECT block,conflicted FROM invisi_finalized_anchors WHERE deployment=?1", s.key).Scan(&raw, &conflicted)
	anchor, e := decodeAnchor(raw, e)
	if e != nil {
		return e
	}
	if conflicted != 0 {
		return model.ErrFinalityConflict
	}
	if guard != nil {
		if release, e = guard(ctx, anchor); e != nil {
			return e
		}
	}
	if anchor != nil {
		var indexed []byte
		err := tx.QueryRow(ctx, "SELECT batch->'block' FROM invisi_blocks WHERE deployment=?1 AND number=?2", s.key, anchor.Number).Scan(&indexed)
		accepted, err := decodeAnchor(indexed, err)
		if err != nil {
			return err
		}
		if accepted != nil && *accepted != *anchor {
			return model.ErrFinalityChanged
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) ApplyRange(ctx context.Context, blocks []model.Batch, guard func(context.Context) (func(), error)) error {
	return s.ApplyRangeWithFinality(ctx, blocks, func(ctx context.Context, _ *model.Block) (func(), error) {
		if guard == nil {
			return nil, nil
		}
		return guard(ctx)
	})
}

// ApplyRangeWithFinality binds the commit guard to this writer's durable anchor.
func (s *Store) ApplyRangeWithFinality(ctx context.Context, blocks []model.Batch, guard func(context.Context, *model.Block) (func(), error)) error {
	if len(blocks) == 0 || len(blocks) > MaxApplyRange {
		return errors.New("invalid block range size")
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	var release func()
	defer func() {
		_ = tx.Rollback(ctx)
		if release != nil {
			release()
		}
	}()
	if e = s.lock(ctx, tx); e != nil {
		return e
	}
	for _, b := range blocks {
		if e = s.apply(ctx, tx, b); e != nil {
			return e
		}
	}
	var raw []byte
	var conflicted int
	e = tx.QueryRow(ctx, "SELECT block,conflicted FROM invisi_finalized_anchors WHERE deployment=$1 FOR UPDATE", s.key).Scan(&raw, &conflicted)
	anchor, e := decodeAnchor(raw, e)
	if e != nil {
		return e
	}
	if conflicted != 0 {
		return model.ErrFinalityConflict
	}
	if guard != nil {
		if release, e = guard(ctx, anchor); e != nil {
			return e
		}
	}
	if anchor != nil {
		var indexed []byte
		err := tx.QueryRow(ctx, "SELECT batch->'block' FROM invisi_blocks WHERE deployment=$1 AND number=$2", s.key, anchor.Number).Scan(&indexed)
		accepted, err := decodeAnchor(indexed, err)
		if err != nil {
			return err
		}
		if accepted != nil && *accepted != *anchor {
			return model.ErrFinalityChanged
		}
	}
	return tx.Commit(ctx)
}

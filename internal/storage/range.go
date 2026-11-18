package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/invisi-labs/invisi-rpc/internal/model"
	"github.com/jackc/pgx/v5"
)

const MaxApplyRange = 128

// ApplyRange publishes a bounded sequence atomically, retaining each block's
// continuity, event, Merkle and root checks. guard must not call this store: it
// runs with the writer transaction held, immediately before the final fence check.
// Its optional publication release is held through commit/rollback.
func (s *SQLiteStore) ApplyRange(ctx context.Context, blocks []model.Batch, guard func(context.Context) (func(), error)) error {
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
	if guard != nil {
		if release, e = guard(ctx); e != nil {
			return e
		}
	}
	var conflicted int
	e = tx.QueryRow(ctx, "SELECT conflicted FROM invisi_finalized_anchors WHERE deployment=?1", s.key).Scan(&conflicted)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if conflicted != 0 {
		return errors.New("finalized history is fenced")
	}
	return tx.Commit(ctx)
}
func (s *Store) ApplyRange(ctx context.Context, blocks []model.Batch, guard func(context.Context) (func(), error)) error {
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
	if guard != nil {
		if release, e = guard(ctx); e != nil {
			return e
		}
	}
	var conflicted int
	e = tx.QueryRow(ctx, "SELECT conflicted FROM invisi_finalized_anchors WHERE deployment=$1 FOR UPDATE", s.key).Scan(&conflicted)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	if conflicted != 0 {
		return errors.New("finalized history is fenced")
	}
	return tx.Commit(ctx)
}

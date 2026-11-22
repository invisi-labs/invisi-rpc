package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/invisi-labs/invisi-rpc/internal/model"
	"github.com/jackc/pgx/v5"
)

func decodeAnchor(data []byte, err error) (*model.Block, error) {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if string(data) == "null" {
		return nil, nil
	}
	var b model.Block
	if err = json.Unmarshal(data, &b); err != nil {
		return nil, err
	}
	return &b, nil
}
func anchorUpdate(previous *model.Block, next model.Block) error {
	if previous != nil && (next.Number < previous.Number || (next.Number == previous.Number && (next.Hash != previous.Hash || next.Timestamp != previous.Timestamp))) {
		return errors.New("accepted finalized anchor cannot regress or change")
	}
	return nil
}
func (s *Store) FinalizedAnchor(ctx context.Context) (*model.Block, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, "SELECT block FROM invisi_finalized_anchors WHERE deployment=$1", s.key).Scan(&raw)
	return decodeAnchor(raw, err)
}
func (s *SQLiteStore) FinalizedAnchor(ctx context.Context) (*model.Block, error) {
	var raw []byte
	err := s.readPool.QueryRow(ctx, "SELECT block FROM invisi_finalized_anchors WHERE deployment=?1", s.key).Scan(&raw)
	return decodeAnchor(raw, err)
}
func (s *Store) SaveFinalizedAnchor(ctx context.Context, b model.Block) error {
	return s.saveFinalizedAnchor(ctx, b, nil, false)
}

// SaveFinalizedAnchorAt accepts only a block in the verified indexed prefix.
func (s *Store) SaveFinalizedAnchorAt(ctx context.Context, b model.Block, expected *model.Block) error {
	return s.saveFinalizedAnchor(ctx, b, expected, true)
}

func (s *Store) saveFinalizedAnchor(ctx context.Context, b model.Block, expected *model.Block, checked bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = s.lock(ctx, tx); err != nil {
		return err
	}

	var raw []byte
	var conflicted int
	queryErr := tx.QueryRow(ctx, "SELECT block,conflicted FROM invisi_finalized_anchors WHERE deployment=$1 FOR UPDATE", s.key).Scan(&raw, &conflicted)
	if conflicted != 0 {
		return model.ErrFinalityConflict
	}
	old, err := decodeAnchor(raw, queryErr)
	if err != nil {
		return err
	}
	if checked {
		head, e := s.checkpoint(ctx, tx)
		if e != nil {
			return e
		}
		if !sameCheckpoint(head, expected) || head == nil || b.Number > head.Number {
			return model.ErrFinalityChanged
		}
		var indexed []byte
		e = tx.QueryRow(ctx, "SELECT batch->'block' FROM invisi_blocks WHERE deployment=$1 AND number=$2", s.key, b.Number).Scan(&indexed)
		matched, e := decodeAnchor(indexed, e)
		if e != nil {
			return e
		}
		if matched == nil || *matched != b {
			return model.ErrFinalityChanged
		}
	}
	if err = anchorUpdate(old, b); err != nil {
		return err
	}
	raw, err = json.Marshal(b)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO invisi_finalized_anchors(deployment,block) VALUES($1,$2) ON CONFLICT(deployment) DO UPDATE SET block=excluded.block", s.key, string(raw)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *SQLiteStore) SaveFinalizedAnchor(ctx context.Context, b model.Block) error {
	return s.saveFinalizedAnchor(ctx, b, nil, false)
}

// SaveFinalizedAnchorAt accepts only a block in the verified indexed prefix.
func (s *SQLiteStore) SaveFinalizedAnchorAt(ctx context.Context, b model.Block, expected *model.Block) error {
	return s.saveFinalizedAnchor(ctx, b, expected, true)
}

func (s *SQLiteStore) saveFinalizedAnchor(ctx context.Context, b model.Block, expected *model.Block, checked bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Acquire the writer before reading the snapshot, including across SQLite handles.
	if _, err = tx.Exec(ctx, "UPDATE invisi_finalized_anchors SET conflicted=conflicted WHERE deployment=?1", s.key); err != nil {
		return err
	}

	var raw []byte
	var conflicted int
	queryErr := tx.QueryRow(ctx, "SELECT block,conflicted FROM invisi_finalized_anchors WHERE deployment=?1", s.key).Scan(&raw, &conflicted)
	if conflicted != 0 {
		return model.ErrFinalityConflict
	}
	old, err := decodeAnchor(raw, queryErr)
	if err != nil {
		return err
	}
	if checked {
		head, e := s.checkpoint(ctx, tx)
		if e != nil {
			return e
		}
		if !sameCheckpoint(head, expected) || head == nil || b.Number > head.Number {
			return model.ErrFinalityChanged
		}
		var indexed []byte
		e = tx.QueryRow(ctx, "SELECT batch->'block' FROM invisi_blocks WHERE deployment=?1 AND number=?2", s.key, b.Number).Scan(&indexed)
		matched, e := decodeAnchor(indexed, e)
		if e != nil {
			return e
		}
		if matched == nil || *matched != b {
			return model.ErrFinalityChanged
		}
	}
	if err = anchorUpdate(old, b); err != nil {
		return err
	}
	raw, err = json.Marshal(b)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO invisi_finalized_anchors(deployment,block) VALUES(?1,?2) ON CONFLICT(deployment) DO UPDATE SET block=excluded.block", s.key, string(raw)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) FinalityConflict(ctx context.Context) (bool, error) {
	var conflicted int
	err := s.pool.QueryRow(ctx, "SELECT conflicted FROM invisi_finalized_anchors WHERE deployment=$1", s.key).Scan(&conflicted)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return conflicted != 0, err
}
func (s *SQLiteStore) FinalityConflict(ctx context.Context) (bool, error) {
	var conflicted int
	err := s.readPool.QueryRow(ctx, "SELECT conflicted FROM invisi_finalized_anchors WHERE deployment=?1", s.key).Scan(&conflicted)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return conflicted != 0, err
}
func (s *Store) MarkFinalityConflict(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = s.lock(ctx, tx); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO invisi_finalized_anchors(deployment,block,conflicted) VALUES($1,'null',1) ON CONFLICT(deployment) DO UPDATE SET conflicted=1", s.key); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *SQLiteStore) MarkFinalityConflict(ctx context.Context) error {
	_, err := s.pool.ExecContext(ctx, "INSERT INTO invisi_finalized_anchors(deployment,block,conflicted) VALUES(?1,'null',1) ON CONFLICT(deployment) DO UPDATE SET conflicted=1", s.key)
	return err
}

func sameCheckpoint(a, b *model.Block) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

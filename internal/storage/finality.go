package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/invisi-lab/invisi-rpc/internal/model"
	"github.com/jackc/pgx/v5"
)

func decodeAnchor(data []byte, err error) (*model.Block, error) {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
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
		return errors.New("finalized history is fenced")
	}
	old, err := decodeAnchor(raw, queryErr)
	if err != nil {
		return err
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
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var raw []byte
	var conflicted int
	queryErr := tx.QueryRow(ctx, "SELECT block,conflicted FROM invisi_finalized_anchors WHERE deployment=?1", s.key).Scan(&raw, &conflicted)
	if conflicted != 0 {
		return errors.New("finalized history is fenced")
	}
	old, err := decodeAnchor(raw, queryErr)
	if err != nil {
		return err
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
	_, err := s.pool.Exec(ctx, "UPDATE invisi_finalized_anchors SET conflicted=1 WHERE deployment=$1", s.key)
	return err
}
func (s *SQLiteStore) MarkFinalityConflict(ctx context.Context) error {
	_, err := s.pool.ExecContext(ctx, "UPDATE invisi_finalized_anchors SET conflicted=1 WHERE deployment=?1", s.key)
	return err
}

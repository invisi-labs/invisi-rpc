package storage

import (
	"context"
	"errors"
	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	"github.com/jackc/pgx/v5"
)

func (s *Store) ConsumedAt(ctx context.Context, hash common.Hash, at uint64, credit bool) (bool, error) {
	table := "invisi_nullifiers"
	if credit {
		table = "invisi_credit_nullifiers"
	}
	var found bool
	e := s.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE deployment=$1 AND hash=$2 AND block_number<=$3)", s.key, hash[:], at).Scan(&found)
	return found, e
}

func (s *Store) CreditStatusAt(ctx context.Context, owner, token common.Address, grantID string, at uint64) (uint8, error) {
	if e := model.ValidateGrantID(grantID); e != nil {
		return 0, e
	}
	var status uint8
	e := s.pool.QueryRow(ctx, "SELECT status FROM invisi_credit_status_events WHERE deployment=$1 AND owner=$2 AND token=$3 AND grant_id=$4 AND block_number<=$5 ORDER BY block_number DESC,log_index DESC LIMIT 1", s.key, owner[:], token[:], grantID, at).Scan(&status)
	if errors.Is(e, pgx.ErrNoRows) {
		return 0, nil
	}
	if e == nil && status > 2 {
		return 0, errors.New("invalid persisted credit status")
	}
	return status, e
}

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
	"github.com/jackc/pgx/v5"
	"math/big"
	"strconv"
)

func (s *Store) CreditStates(ctx context.Context, from, to uint64, limit int) ([]model.CreditState, error) {
	raw, e := s.items(ctx, "creditStates", from, to, limit)
	if e != nil {
		return nil, e
	}
	out := make([]model.CreditState, 0, len(raw))
	for _, r := range raw {
		var v model.CreditState
		if e = json.Unmarshal(r, &v); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}
func (s *Store) ConsumedAt(ctx context.Context, hash common.Hash, at uint64, credit bool) (bool, error) {
	table := "invisi_nullifiers"
	if credit {
		table = "invisi_credit_nullifiers"
	}
	var found bool
	e := s.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE deployment=$1 AND hash=$2 AND block_number<=$3)", s.key, hash[:], at).Scan(&found)
	return found, e
}

func (s *Store) CreditAllocationAt(ctx context.Context, owner, token common.Address, at uint64) (string, uint16, error) {
	var shard string
	var slot uint16
	e := s.pool.QueryRow(ctx, "SELECT shard,slot FROM invisi_credit_grants WHERE deployment=$1 AND owner=$2 AND token=$3 AND kind='CreditCreated' AND block_number<=$4 ORDER BY block_number DESC,log_index DESC LIMIT 1", s.key, owner[:], token[:], at).Scan(&shard, &slot)
	if errors.Is(e, pgx.ErrNoRows) {
		return "1", 0, nil
	}
	if e != nil {
		return "", 0, e
	}
	if slot < 65535 {
		return shard, slot + 1, nil
	}
	n, e := strconv.ParseUint(shard, 10, 64)
	if e != nil || n == ^uint64(0) {
		return "", 0, errors.New("allocation exhausted")
	}
	return strconv.FormatUint(n+1, 10), 0, nil
}
func (s *Store) CreditBitmapAt(ctx context.Context, owner, token common.Address, shard string, word uint8, at uint64) (string, error) {
	rows, e := s.pool.Query(ctx, "SELECT slot FROM invisi_credit_grants WHERE deployment=$1 AND owner=$2 AND token=$3 AND shard=$4 AND kind='CreditRevoked' AND slot BETWEEN $5 AND $6 AND block_number<=$7", s.key, owner[:], token[:], shard, int(word)*256, int(word)*256+255, at)
	if e != nil {
		return "", e
	}
	defer rows.Close()
	bits := new(big.Int)
	for rows.Next() {
		var slot uint16
		if e = rows.Scan(&slot); e != nil {
			return "", e
		}
		bits.SetBit(bits, int(slot%256), 1)
	}
	return bits.String(), rows.Err()
}

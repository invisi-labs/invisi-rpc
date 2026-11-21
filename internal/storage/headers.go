package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	"github.com/jackc/pgx/v5"
)

const MaxHeaderRange = 256
const maxHeaderBytes = 7 << 20 // Leave room for the public response envelope.
var ErrHeadersUnavailable = errors.New("complete indexed headers unavailable")

func headerRangeBounds(from, to uint64) error {
	if to < from || to > math.MaxInt64 || to-from >= MaxHeaderRange {
		return errors.New("invalid historical header range")
	}
	return nil
}

type headerRows interface {
	Next() bool
	Scan(...any) error
	Err() error
}

// A single SELECT binds the rows and conflict fence to one database snapshot.
// Read even rows with absent headers so corruption cannot become a fallback.
func decodeHeaders(rows headerRows, from, to uint64) ([]model.BlockHeader, error) {
	out := make([]model.BlockHeader, 0, to-from+1)
	missing := false
	total := 0
	var parent common.Hash
	count := uint64(0)
	for rows.Next() {
		var number *uint64
		var rawBlock, rawHeader string
		var hash, parentHash []byte
		var fenced int
		if e := rows.Scan(&number, &hash, &parentHash, &rawBlock, &rawHeader, &fenced); e != nil {
			return nil, e
		}
		if fenced != 0 {
			return nil, errors.New("finalized history is fenced")
		}
		if number == nil {
			continue
		}
		if *number != from+count {
			return nil, errors.New("incomplete or unordered indexed header range")
		}
		total += len(rawHeader)
		if total > maxHeaderBytes {
			return nil, ErrQueryLimit
		}
		var b model.Batch
		if e := json.Unmarshal([]byte(rawBlock), &b.Block); e != nil {
			return nil, e
		}
		if b.Block.Number != *number || len(hash) != 32 || len(parentHash) != 32 || b.Block.Hash != common.BytesToHash(hash) || b.Block.ParentHash != common.BytesToHash(parentHash) {
			return nil, errors.New("corrupt indexed header identity")
		}
		if count > 0 && b.Block.ParentHash != parent {
			return nil, errors.New("indexed header parent mismatch")
		}
		if rawHeader == "null" || rawHeader == "" {
			missing = true
		} else {
			var e error
			if b.Header, e = model.DecodeCompleteHeader([]byte(rawHeader)); e != nil {
				return nil, e
			}
			if e := b.ValidateHeader(); e != nil {
				return nil, e
			}
			// Header JSON has a redundant hash: detect damage to it as well.
			var declared struct {
				Hash *common.Hash `json:"hash"`
			}
			if e := json.Unmarshal([]byte(rawHeader), &declared); e != nil {
				return nil, e
			}
			if b.Header == nil || declared.Hash == nil || *declared.Hash != b.Block.Hash {
				return nil, errors.New("corrupt stored header hash")
			}
			out = append(out, model.BlockHeader{Header: b.Header, HeaderHash: b.Block.Hash})
		}
		parent = b.Block.Hash
		count++
	}
	if e := rows.Err(); e != nil {
		return nil, e
	}
	if missing || count != to-from+1 {
		return nil, ErrHeadersUnavailable
	}
	return out, nil
}

func (s *Store) Headers(ctx context.Context, from, to uint64) ([]model.BlockHeader, error) {
	if e := headerRangeBounds(from, to); e != nil {
		return nil, e
	}
	rows, e := s.pool.Query(ctx, `SELECT b.number,b.hash,b.parent_hash,COALESCE((b.batch->'block')::text,'null'),COALESCE((b.batch->'header')::text,'null'),f.conflicted FROM (SELECT COALESCE((SELECT conflicted FROM invisi_finalized_anchors WHERE deployment=$1),0) AS conflicted) f LEFT JOIN invisi_blocks b ON b.deployment=$1 AND b.number BETWEEN $2 AND $3 ORDER BY b.number`, s.key, from, to)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	return decodeHeaders(rows, from, to)
}
func (s *SQLiteStore) Headers(ctx context.Context, from, to uint64) ([]model.BlockHeader, error) {
	if e := headerRangeBounds(from, to); e != nil {
		return nil, e
	}
	rows, e := s.readPool.Query(ctx, `SELECT b.number,b.hash,b.parent_hash,COALESCE(json_extract(b.batch,'$.block'),'null'),COALESCE(b.batch->'$.header','null'),f.conflicted FROM (SELECT COALESCE((SELECT conflicted FROM invisi_finalized_anchors WHERE deployment=?1),0) AS conflicted) f LEFT JOIN invisi_blocks b ON b.deployment=?1 AND b.number BETWEEN ?2 AND ?3 ORDER BY b.number`, s.key, from, to)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	return decodeHeaders(rows, from, to)
}

func checkCacheHeaders(headers []*types.Header) error {
	if len(headers) == 0 || len(headers) > MaxHeaderRange {
		return errors.New("invalid header cache range")
	}
	total := 0
	for i, h := range headers {
		if h == nil || h.Number == nil || !h.Number.IsUint64() || h.Number.BitLen() > 63 {
			return errors.New("invalid cache header height")
		}
		if i > 0 && (h.Number.Uint64() != headers[i-1].Number.Uint64()+1 || h.ParentHash != headers[i-1].Hash()) {
			return errors.New("invalid cache header sequence")
		}
		raw, e := json.Marshal(h)
		if e != nil {
			return e
		}
		total += len(raw)
		if total > maxHeaderBytes {
			return ErrQueryLimit
		}
	}
	return nil
}

// CacheHeaders fills only existing matching block identities under the same
// writer/rollback lock. It never reapplies events, roots or nullifiers.
func (s *Store) CacheHeaders(ctx context.Context, headers []*types.Header, guard func(context.Context) error) error {
	if e := checkCacheHeaders(headers); e != nil {
		return e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = s.lock(ctx, tx); e != nil {
		return e
	}
	for _, h := range headers {
		var raw []byte
		e = tx.QueryRow(ctx, "SELECT batch FROM invisi_blocks WHERE deployment=$1 AND number=$2", s.key, h.Number.Uint64()).Scan(&raw)
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrHeadersUnavailable
		}
		if e != nil {
			return e
		}
		var b model.Batch
		if e = json.Unmarshal(raw, &b); e != nil {
			return e
		}
		if e = b.ValidateHeader(); e != nil {
			return e
		}
		b.Header = h
		if e = b.ValidateHeader(); e != nil {
			return e
		}
		data, e := json.Marshal(h)
		if e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, "UPDATE invisi_blocks SET batch=jsonb_set(batch,'{header}',$3::jsonb) WHERE deployment=$1 AND number=$2 AND hash=$4", s.key, h.Number.Uint64(), data, h.Hash().Bytes())
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return errors.New("header backfill storage identity mismatch")
		}
	}
	if guard != nil {
		if e = guard(ctx); e != nil {
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
func (s *SQLiteStore) CacheHeaders(ctx context.Context, headers []*types.Header, guard func(context.Context) error) error {
	if e := checkCacheHeaders(headers); e != nil {
		return e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	for _, h := range headers {
		var raw []byte
		e = tx.QueryRow(ctx, "SELECT batch FROM invisi_blocks WHERE deployment=?1 AND number=?2", s.key, h.Number.Uint64()).Scan(&raw)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrHeadersUnavailable
		}
		if e != nil {
			return e
		}
		var b model.Batch
		if e = json.Unmarshal(raw, &b); e != nil {
			return e
		}
		if e = b.ValidateHeader(); e != nil {
			return e
		}
		b.Header = h
		if e = b.ValidateHeader(); e != nil {
			return e
		}
		data, e := json.Marshal(h)
		if e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, "UPDATE invisi_blocks SET batch=json_set(batch,'$.header',json(?3)) WHERE deployment=?1 AND number=?2 AND hash=?4", s.key, h.Number.Uint64(), string(data), h.Hash().Bytes())
		if e != nil {
			return e
		}
		changed, e := tag.RowsAffected()
		if e != nil {
			return e
		}
		if changed != 1 {
			return errors.New("header backfill storage identity mismatch")
		}
	}
	if guard != nil {
		if e = guard(ctx); e != nil {
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

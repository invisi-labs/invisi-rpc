package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-rpc/internal/model"
	"github.com/jackc/pgx/v5"
)

// Full state is materialized after every event, so availability cannot revive a
// stale profile and filtering always applies after selecting the latest state.
const broadcasterSQL = `WITH ranked AS (
 SELECT operator,value,row_number() OVER(PARTITION BY operator ORDER BY block_number DESC,log_index DESC) AS rank
 FROM invisi_broadcasters WHERE deployment=P1 AND block_number<=P2
 ) SELECT value FROM ranked WHERE rank=1 AND operator>P3
 AND (P4=0 OR ENABLED=P5) ORDER BY operator LIMIT P6`

func broadcasterQuerySQL(pg bool) string {
	q := broadcasterSQL
	enabled := "json_extract(value,'$.enabled')"
	prefix := "?"
	if pg {
		enabled = "(value->>'enabled')::boolean"
		prefix = "$"
	}
	q = strings.ReplaceAll(q, "ENABLED", enabled)
	for i := 1; i <= 6; i++ {
		q = strings.ReplaceAll(q, fmt.Sprint("P", i), fmt.Sprint(prefix, i))
	}
	return q
}
func broadcasterProfileDecision(saved *string, version *int, hasBlocks bool, requested string) error {
	if saved != nil && *saved != requested {
		return errors.New("broadcaster registry deployment differs from persisted configuration; rebuild index")
	}
	if version != nil && *version != 1 {
		return errors.New("unsupported broadcaster history format; rebuild index")
	}
	if requested != "null" && hasBlocks && (saved == nil || version == nil) {
		return errors.New("broadcaster coverage missing from existing index; rebuild index")
	}
	return nil
}
func (s *Store) ensureBroadcasterProfile(ctx context.Context, tx pgx.Tx) error {
	profile, _ := json.Marshal(s.deployment.BroadcasterRegistry)
	var saved string
	var savedPtr *string
	err := tx.QueryRow(ctx, "SELECT profile FROM invisi_broadcaster_profiles WHERE deployment=$1", s.key).Scan(&saved)
	if err == nil {
		savedPtr = &saved
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var version int
	var versionPtr *int
	err = tx.QueryRow(ctx, "SELECT version FROM invisi_broadcaster_formats WHERE deployment=$1", s.key).Scan(&version)
	if err == nil {
		versionPtr = &version
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var blocks bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM invisi_blocks WHERE deployment=$1)", s.key).Scan(&blocks); err != nil {
		return err
	}
	if err = broadcasterProfileDecision(savedPtr, versionPtr, blocks, string(profile)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO invisi_broadcaster_profiles VALUES($1,$2) ON CONFLICT DO NOTHING", s.key, string(profile)); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO invisi_broadcaster_formats VALUES($1,1) ON CONFLICT DO NOTHING", s.key)
	return err
}
func (s *SQLiteStore) ensureBroadcasterProfile(ctx context.Context, tx *sqliteTx) error {
	profile, _ := json.Marshal(s.deployment.BroadcasterRegistry)
	var saved string
	var savedPtr *string
	err := tx.QueryRow(ctx, "SELECT profile FROM invisi_broadcaster_profiles WHERE deployment=?1", s.key).Scan(&saved)
	if err == nil {
		savedPtr = &saved
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var version int
	var versionPtr *int
	err = tx.QueryRow(ctx, "SELECT version FROM invisi_broadcaster_formats WHERE deployment=?1", s.key).Scan(&version)
	if err == nil {
		versionPtr = &version
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var blocks bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM invisi_blocks WHERE deployment=?1)", s.key).Scan(&blocks); err != nil {
		return err
	}
	if err = broadcasterProfileDecision(savedPtr, versionPtr, blocks, string(profile)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO invisi_broadcaster_profiles VALUES(?1,?2) ON CONFLICT DO NOTHING", s.key, string(profile)); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO invisi_broadcaster_formats VALUES(?1,1) ON CONFLICT DO NOTHING", s.key)
	return err
}
func broadcasterState(update model.BroadcasterUpdate, prior *model.RegisteredBroadcaster, block model.Block) (model.RegisteredBroadcaster, error) {
	if update.Operator == (common.Address{}) || uint64(update.LogIndex) > math.MaxInt64 {
		return model.RegisteredBroadcaster{}, errors.New("invalid broadcaster operator or log position")
	}
	if update.Enabled != nil {
		if prior == nil {
			return model.RegisteredBroadcaster{}, errors.New("availability without registered broadcaster")
		}
		if update.Endpoint != "" || update.MetadataURI != "" || update.QuoteSigner != (common.Address{}) || update.FeeRecipient != (common.Address{}) {
			return model.RegisteredBroadcaster{}, errors.New("availability carries profile fields")
		}
		state := *prior
		state.Enabled = *update.Enabled
		state.Block = block
		return state, nil
	}
	if update.QuoteSigner == (common.Address{}) || update.FeeRecipient == (common.Address{}) || len(update.Endpoint) <= 8 || len(update.Endpoint) > 2048 || !strings.HasPrefix(update.Endpoint, "https://") || len(update.MetadataURI) > 2048 {
		return model.RegisteredBroadcaster{}, errors.New("invalid broadcaster profile")
	}
	for _, uri := range []string{update.Endpoint, update.MetadataURI} {
		for _, b := range []byte(uri) {
			if b <= 0x20 || b >= 0x7f {
				return model.RegisteredBroadcaster{}, errors.New("broadcaster URI must be printable ASCII")
			}
		}
	}
	enabled := true
	if prior != nil {
		enabled = prior.Enabled
	}
	return model.RegisteredBroadcaster{Operator: update.Operator, Endpoint: update.Endpoint, MetadataURI: update.MetadataURI, QuoteSigner: update.QuoteSigner, FeeRecipient: update.FeeRecipient, Enabled: enabled, Block: block}, nil
}
func (s *Store) applyBroadcasters(ctx context.Context, tx pgx.Tx, b model.Batch) error {
	var last uint
	for i, u := range b.Broadcasters {
		if s.deployment.BroadcasterRegistry == nil || b.Block.Number < s.deployment.BroadcasterRegistry.FromBlock {
			return errors.New("broadcaster event outside configured deployment")
		}
		if i > 0 && u.LogIndex <= last {
			return errors.New("broadcaster events out of order")
		}
		last = u.LogIndex
		var prior *model.RegisteredBroadcaster
		var raw []byte
		err := tx.QueryRow(ctx, "SELECT value FROM invisi_broadcasters WHERE deployment=$1 AND operator=$2 ORDER BY block_number DESC,log_index DESC LIMIT 1", s.key, u.Operator[:]).Scan(&raw)
		if err == nil {
			prior = new(model.RegisteredBroadcaster)
			if err = json.Unmarshal(raw, prior); err != nil {
				return err
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		state, err := broadcasterState(u, prior, b.Block)
		if err != nil {
			return err
		}
		raw, err = json.Marshal(state)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO invisi_broadcasters VALUES($1,$2,$3,$4,'state',$5)", s.key, u.Operator[:], b.Block.Number, u.LogIndex, raw); err != nil {
			return err
		}
	}
	return nil
}
func (s *SQLiteStore) applyBroadcasters(ctx context.Context, tx *sqliteTx, b model.Batch) error {
	var last uint
	for i, u := range b.Broadcasters {
		if s.deployment.BroadcasterRegistry == nil || b.Block.Number < s.deployment.BroadcasterRegistry.FromBlock {
			return errors.New("broadcaster event outside configured deployment")
		}
		if i > 0 && u.LogIndex <= last {
			return errors.New("broadcaster events out of order")
		}
		last = u.LogIndex
		var prior *model.RegisteredBroadcaster
		var raw []byte
		err := tx.QueryRow(ctx, "SELECT value FROM invisi_broadcasters WHERE deployment=?1 AND operator=?2 ORDER BY block_number DESC,log_index DESC LIMIT 1", s.key, u.Operator[:]).Scan(&raw)
		if err == nil {
			prior = new(model.RegisteredBroadcaster)
			if err = json.Unmarshal(raw, prior); err != nil {
				return err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		state, err := broadcasterState(u, prior, b.Block)
		if err != nil {
			return err
		}
		raw, err = json.Marshal(state)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO invisi_broadcasters VALUES(?1,?2,?3,?4,'state',?5)", s.key, u.Operator[:], b.Block.Number, u.LogIndex, string(raw)); err != nil {
			return err
		}
	}
	return nil
}

func broadcasterAnchor(d model.Deployment, q model.BroadcasterQuery, head *model.Block, canonical *model.Block) (*model.Block, error) {
	if d.BroadcasterRegistry == nil {
		return nil, errors.New("deployment has no broadcaster registry")
	}
	if head == nil || head.Number < d.BroadcasterRegistry.FromBlock {
		return nil, errors.New("broadcaster index has not reached deployment")
	}
	if q.After != (common.Address{}) && q.Block == nil {
		return nil, errors.New("pagination requires block anchor")
	}
	if q.Block == nil {
		return head, nil
	}
	if q.Block.Number < d.BroadcasterRegistry.FromBlock || canonical == nil || *canonical != *q.Block {
		return nil, errors.New("unknown or reorganized broadcaster anchor")
	}
	return canonical, nil
}
func normalizeBroadcasterQuery(q model.BroadcasterQuery) (model.BroadcasterQuery, error) {
	if q.Limit == 0 {
		q.Limit = 100
	}
	if q.Limit < 1 || q.Limit > 100 {
		return q, errors.New("broadcaster limit must be 1..100")
	}
	if q.Block != nil && q.Block.Number > math.MaxInt64 {
		return q, errors.New("broadcaster anchor out of range")
	}
	return q, nil
}
func finishBroadcaster(page *model.BroadcasterPage, limit int) {
	if len(page.Broadcasters) > limit {
		page.Broadcasters = page.Broadcasters[:limit]
		cursor := page.Broadcasters[limit-1].Operator
		page.Next = &cursor
	}
}
func (s *Store) Broadcasters(ctx context.Context, query model.BroadcasterQuery) (*model.BroadcasterPage, error) {
	q, err := normalizeBroadcasterQuery(query)
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	head, err := s.checkpoint(ctx, tx)
	if err != nil {
		return nil, err
	}
	var canonical *model.Block
	if q.Block != nil {
		var raw []byte
		err = tx.QueryRow(ctx, "SELECT batch->'block' FROM invisi_blocks WHERE deployment=$1 AND number=$2", s.key, q.Block.Number).Scan(&raw)
		if err == nil {
			canonical = new(model.Block)
			err = json.Unmarshal(raw, canonical)
		} else if errors.Is(err, pgx.ErrNoRows) {
			err = nil
		}
		if err != nil {
			return nil, err
		}
	}
	anchor, err := broadcasterAnchor(s.deployment, q, head, canonical)
	if err != nil {
		return nil, err
	}
	filter := 0
	enabled := false
	if q.Enabled != nil {
		filter = 1
		enabled = *q.Enabled
	}
	rows, err := tx.Query(ctx, broadcasterQuerySQL(true), s.key, anchor.Number, q.After[:], filter, enabled, q.Limit+1)
	if err != nil {
		return nil, err
	}
	page := &model.BroadcasterPage{Block: *anchor, Broadcasters: []model.RegisteredBroadcaster{}}
	for rows.Next() {
		var raw []byte
		var state model.RegisteredBroadcaster
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &state)
		}
		if err != nil {
			rows.Close()
			return nil, err
		}
		page.Broadcasters = append(page.Broadcasters, state)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	finishBroadcaster(page, q.Limit)
	return page, nil
}
func (s *SQLiteStore) Broadcasters(ctx context.Context, query model.BroadcasterQuery) (*model.BroadcasterPage, error) {
	q, err := normalizeBroadcasterQuery(query)
	if err != nil {
		return nil, err
	}
	tx, err := s.readPool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	head, err := s.checkpoint(ctx, tx)
	if err != nil {
		return nil, err
	}
	var canonical *model.Block
	if q.Block != nil {
		var raw []byte
		err = tx.QueryRow(ctx, "SELECT batch->'block' FROM invisi_blocks WHERE deployment=?1 AND number=?2", s.key, q.Block.Number).Scan(&raw)
		if err == nil {
			canonical = new(model.Block)
			err = json.Unmarshal(raw, canonical)
		} else if errors.Is(err, sql.ErrNoRows) {
			err = nil
		}
		if err != nil {
			return nil, err
		}
	}
	anchor, err := broadcasterAnchor(s.deployment, q, head, canonical)
	if err != nil {
		return nil, err
	}
	filter := 0
	enabled := false
	if q.Enabled != nil {
		filter = 1
		enabled = *q.Enabled
	}
	rows, err := tx.Query(ctx, broadcasterQuerySQL(false), s.key, anchor.Number, q.After[:], filter, enabled, q.Limit+1)
	if err != nil {
		return nil, err
	}
	page := &model.BroadcasterPage{Block: *anchor, Broadcasters: []model.RegisteredBroadcaster{}}
	for rows.Next() {
		var raw []byte
		var state model.RegisteredBroadcaster
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &state)
		}
		if err != nil {
			rows.Close()
			return nil, err
		}
		page.Broadcasters = append(page.Broadcasters, state)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	finishBroadcaster(page, q.Limit)
	return page, nil
}

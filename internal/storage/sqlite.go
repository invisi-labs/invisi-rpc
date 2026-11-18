// Package storage includes a local SQLite backend using WAL snapshots and atomic writes.
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-labs/invisi-rpc/internal/cryptography"
	"github.com/invisi-labs/invisi-rpc/internal/merkle"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	_ "modernc.org/sqlite"
)

type sqliteDB struct{ *sql.DB }
type sqliteTx struct{ *sql.Tx }

func (d *sqliteDB) QueryRow(c context.Context, q string, a ...any) *sql.Row {
	return d.QueryRowContext(c, q, a...)
}
func (d *sqliteDB) Query(c context.Context, q string, a ...any) (*sql.Rows, error) {
	return d.QueryContext(c, q, a...)
}
func (d *sqliteDB) Begin(c context.Context) (*sqliteTx, error) {
	t, e := d.BeginTx(c, nil)
	if e != nil {
		return nil, e
	}
	return &sqliteTx{t}, nil
}
func (t *sqliteTx) QueryRow(c context.Context, q string, a ...any) *sql.Row {
	return t.QueryRowContext(c, q, a...)
}
func (t *sqliteTx) Query(c context.Context, q string, a ...any) (*sql.Rows, error) {
	return t.QueryContext(c, q, a...)
}
func (t *sqliteTx) Exec(c context.Context, q string, a ...any) (sql.Result, error) {
	return t.ExecContext(c, q, a...)
}
func (t *sqliteTx) Commit(context.Context) error   { return t.Tx.Commit() }
func (t *sqliteTx) Rollback(context.Context) error { return t.Tx.Rollback() }

// SQLiteStore serializes writers while independent WAL readers see consistent snapshots.
var _ Backend = (*SQLiteStore)(nil)

type SQLiteStore struct {
	pool, readPool *sqliteDB
	key            string
	deployment     model.Deployment
	ValidateRoot   func(context.Context, model.Root) error
}

func (s *SQLiteStore) Close()                                                     { s.readPool.Close(); s.pool.Close() }
func (s *SQLiteStore) SetRootValidator(v func(context.Context, model.Root) error) { s.ValidateRoot = v }
func OpenSQLite(ctx context.Context, path string, d model.Deployment) (*SQLiteStore, error) {
	if path == "" || path == ":memory:" {
		return nil, errors.New("SQLite requires a persistent file path")
	}
	if d.ChainID == "" || d.FromBlock > math.MaxInt64 {
		return nil, errors.New("invalid deployment configuration")
	}
	absolute, e := filepath.Abs(path)
	if e != nil {
		return nil, e
	}
	u := url.URL{Scheme: "file", Path: absolute}
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	u.RawQuery = q.Encode()
	reader, e := sql.Open("sqlite", u.String())
	if e != nil {
		return nil, e
	}
	reader.SetMaxOpenConns(4)
	q.Set("_txlock", "immediate")
	u.RawQuery = q.Encode()
	writer, e := sql.Open("sqlite", u.String())
	if e != nil {
		reader.Close()
		return nil, e
	}
	writer.SetMaxOpenConns(1)
	s := &SQLiteStore{pool: &sqliteDB{writer}, readPool: &sqliteDB{reader}, key: d.ChainID + ":" + strings.ToLower(d.Protocol.Hex()), deployment: d}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		s.Close()
		return nil, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, sqliteSchema); e == nil {
		_, e = tx.Exec(ctx, "INSERT INTO invisi_deployments VALUES(?1,?2,?3) ON CONFLICT DO NOTHING", s.key, d.FromBlock, d.RuntimeCodeHash[:])
	}
	if e == nil {
		var from uint64
		var hash []byte
		e = tx.QueryRow(ctx, "SELECT from_block,runtime_code_hash FROM invisi_deployments WHERE deployment=?1", s.key).Scan(&from, &hash)
		if e == nil && (from != d.FromBlock || common.BytesToHash(hash) != d.RuntimeCodeHash) {
			e = errors.New("deployment configuration differs from persisted configuration")
		}
	}
	if e == nil {
		profile, _ := json.Marshal(d.Registry)
		_, e = tx.Exec(ctx, "INSERT INTO invisi_registry_profiles VALUES(?1,?2) ON CONFLICT DO NOTHING", s.key, string(profile))
		if e == nil {
			var saved string
			e = tx.QueryRow(ctx, "SELECT profile FROM invisi_registry_profiles WHERE deployment=?1", s.key).Scan(&saved)
			if e == nil && saved != string(profile) {
				e = errors.New("registry deployment differs from persisted configuration")
			}
		}
	}
	if e == nil {
		e = s.ensureBroadcasterProfile(ctx, tx)
	}
	if e == nil {
		_, e = tx.Exec(ctx, "INSERT INTO invisi_index_formats VALUES(?1,3) ON CONFLICT DO NOTHING", s.key)
	}
	if e == nil {
		var version int
		e = tx.QueryRow(ctx, "SELECT version FROM invisi_index_formats WHERE deployment=?1", s.key).Scan(&version)
		if e == nil && version != 3 {
			e = errors.New("unsupported index format; use a fresh database and rebuild the event index")
		}
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		tx.Rollback(ctx)
		s.Close()
		return nil, e
	}
	return s, nil
}

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS invisi_finalized_anchors (deployment text PRIMARY KEY, block text NOT NULL, conflicted integer NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS invisi_registrations (
 deployment text NOT NULL, owner BLOB NOT NULL, block_number bigint NOT NULL, log_index bigint NOT NULL, registration TEXT NOT NULL,
 PRIMARY KEY(deployment,owner,block_number,log_index), FOREIGN KEY(deployment,block_number) REFERENCES invisi_blocks ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS invisi_registry_profiles (deployment text PRIMARY KEY, profile text NOT NULL);
CREATE TABLE IF NOT EXISTS invisi_blocks (
 deployment TEXT NOT NULL, number INTEGER NOT NULL, hash BLOB NOT NULL, parent_hash BLOB NOT NULL, batch TEXT NOT NULL,
 PRIMARY KEY(deployment,number)
);
CREATE TABLE IF NOT EXISTS invisi_trees (
 deployment TEXT NOT NULL, tree_id INTEGER NOT NULL, block_number INTEGER NOT NULL,
 PRIMARY KEY(deployment,tree_id), FOREIGN KEY(deployment,block_number) REFERENCES invisi_blocks ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS invisi_roots (
 deployment TEXT NOT NULL, tree_id INTEGER NOT NULL, leaf_count INTEGER NOT NULL, root BLOB NOT NULL, block_number INTEGER NOT NULL, sequence INTEGER NOT NULL,
 PRIMARY KEY(deployment,tree_id,leaf_count), FOREIGN KEY(deployment,tree_id) REFERENCES invisi_trees ON DELETE CASCADE,
 FOREIGN KEY(deployment,block_number) REFERENCES invisi_blocks ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS invisi_roots_lookup ON invisi_roots(deployment,tree_id,root);
CREATE TABLE IF NOT EXISTS invisi_nodes (
 deployment TEXT NOT NULL, tree_id INTEGER NOT NULL, level integer NOT NULL, node_index INTEGER NOT NULL, leaf_count INTEGER NOT NULL, hash BLOB NOT NULL, block_number INTEGER NOT NULL,
 PRIMARY KEY(deployment,tree_id,level,node_index,leaf_count), FOREIGN KEY(deployment,tree_id) REFERENCES invisi_trees ON DELETE CASCADE,
 FOREIGN KEY(deployment,block_number) REFERENCES invisi_blocks ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS invisi_commitment_lookup ON invisi_nodes(deployment,hash) WHERE level=0;
CREATE TABLE IF NOT EXISTS invisi_nullifiers (
 deployment TEXT NOT NULL, hash BLOB NOT NULL, block_number INTEGER NOT NULL,
 PRIMARY KEY(deployment,hash), FOREIGN KEY(deployment,block_number) REFERENCES invisi_blocks ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS invisi_deployments (
 deployment TEXT PRIMARY KEY, from_block INTEGER NOT NULL, runtime_code_hash BLOB NOT NULL
);

CREATE TABLE IF NOT EXISTS invisi_credit_grants (
 deployment text NOT NULL, owner blob NOT NULL, token blob NOT NULL, block_number bigint NOT NULL, log_index bigint NOT NULL,
 kind text NOT NULL, shard text NOT NULL, slot integer NOT NULL,
 PRIMARY KEY(deployment,block_number,log_index), FOREIGN KEY(deployment,block_number) REFERENCES invisi_blocks ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS invisi_credit_grant_lookup ON invisi_credit_grants(deployment,owner,token,kind,block_number,log_index);

CREATE TABLE IF NOT EXISTS invisi_credit_nullifiers (
 deployment TEXT NOT NULL, hash BLOB NOT NULL, block_number INTEGER NOT NULL,
 PRIMARY KEY(deployment,hash), FOREIGN KEY(deployment,block_number) REFERENCES invisi_blocks ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS invisi_index_formats (deployment TEXT PRIMARY KEY, version INTEGER NOT NULL);

CREATE TABLE IF NOT EXISTS invisi_broadcasters (
 deployment text NOT NULL, operator blob NOT NULL, block_number bigint NOT NULL, log_index bigint NOT NULL, kind text NOT NULL, value text NOT NULL,
 PRIMARY KEY(deployment,operator,block_number,log_index), FOREIGN KEY(deployment,block_number) REFERENCES invisi_blocks ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS invisi_broadcaster_profiles (deployment text PRIMARY KEY, profile text NOT NULL);
CREATE TABLE IF NOT EXISTS invisi_broadcaster_formats (deployment text PRIMARY KEY, version integer NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS invisi_broadcaster_positions ON invisi_broadcasters(deployment,block_number,log_index);
`

type sqliteQuerier interface {
	QueryRow(context.Context, string, ...any) *sql.Row
}

func (s *SQLiteStore) checkpoint(ctx context.Context, q sqliteQuerier) (*model.Block, error) {
	var b model.Block
	var raw []byte
	err := q.QueryRow(ctx, "SELECT batch->'block' FROM invisi_blocks WHERE deployment=?1 ORDER BY number DESC LIMIT 1", s.key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(raw, &b)
	return &b, err
}
func (s *SQLiteStore) Checkpoint(ctx context.Context) (*model.Block, error) {
	return s.checkpoint(ctx, s.readPool)
}

// Batch returns the complete persisted block and decoded/raw event material.
func (s *SQLiteStore) Batch(ctx context.Context, number uint64) (*model.Batch, error) {
	if number > math.MaxInt64 {
		return nil, errors.New("block number out of range")
	}
	var data []byte
	err := s.readPool.QueryRow(ctx, "SELECT batch FROM invisi_blocks WHERE deployment=?1 AND number=?2", s.key, number).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var b model.Batch
	if err = json.Unmarshal(data, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

func (s *SQLiteStore) Block(ctx context.Context, number uint64) (*model.Block, error) {
	if number > math.MaxInt64 {
		return nil, errors.New("block number out of range")
	}
	var b model.Block
	var raw []byte
	err := s.readPool.QueryRow(ctx, "SELECT batch->'block' FROM invisi_blocks WHERE deployment=?1 AND number=?2", s.key, number).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(raw, &b)
	return &b, err
}
func (s *SQLiteStore) Apply(ctx context.Context, b model.Batch) error {
	if b.Block.Number > math.MaxInt64 {
		return errors.New("block number out of range")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = s.apply(ctx, tx, b); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *SQLiteStore) apply(ctx context.Context, tx *sqliteTx, b model.Batch) error {
	if b.Block.Number > math.MaxInt64 {
		return errors.New("block number out of range")
	}
	var err error
	var old []byte
	err = tx.QueryRow(ctx, "SELECT hash FROM invisi_blocks WHERE deployment=?1 AND number=?2", s.key, b.Block.Number).Scan(&old)
	if err == nil {
		if common.BytesToHash(old) == b.Block.Hash {
			return nil
		}
		return errors.New("conflicting block hash")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	head, err := s.checkpoint(ctx, tx)
	if err != nil {
		return err
	}
	if head == nil {
		if b.Block.Number != s.deployment.IndexFromBlock() {
			return fmt.Errorf("first block must be %d", s.deployment.IndexFromBlock())
		}
	} else if b.Block.Number != head.Number+1 || b.Block.ParentHash != head.Hash {
		return errors.New("block gap or parent hash mismatch")
	}
	b.BindBlockContext()
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO invisi_blocks VALUES(?1,?2,?3,?4,?5)", s.key, b.Block.Number, b.Block.Hash[:], b.Block.ParentHash[:], raw); err != nil {
		return err
	}
	if err = s.applyBroadcasters(ctx, tx, b); err != nil {
		return err
	}
	for _, registration := range b.Registrations {
		raw, err := json.Marshal(registration)
		if err != nil {
			return err
		}
		if s.deployment.Registry == nil {
			return errors.New("registry event without configured registry")
		}
		if b.Block.Number < s.deployment.Registry.FromBlock {
			return errors.New("registry event predates deployment")
		}
		if _, err = tx.Exec(ctx, "INSERT INTO invisi_registrations VALUES(?1,?2,?3,?4,?5)", s.key, registration.WalletIdentity[:], b.Block.Number, registration.LogIndex, raw); err != nil {
			return err
		}
	}
	for _, id := range b.CreatedTrees {
		if id > math.MaxInt64 {
			return errors.New("tree ID out of range")
		}
		if _, err = tx.Exec(ctx, "INSERT INTO invisi_trees VALUES(?1,?2,?3)", s.key, id, b.Block.Number); err != nil {
			return err
		}
		root := cryptography.EmptyNodes[cryptography.TreeDepth]
		if _, err = tx.Exec(ctx, "INSERT INTO invisi_roots VALUES(?1,?2,0,?3,?4,0)", s.key, id, root[:], b.Block.Number); err != nil {
			return err
		}
	}
	for sequence, in := range b.Insertions {
		if err = s.insert(ctx, tx, b.Block.Number, uint64(sequence+1), in); err != nil {
			return err
		}
	}
	if s.ValidateRoot != nil {
		rows, e := tx.Query(ctx, "SELECT tree_id,leaf_count,root FROM invisi_roots WHERE deployment=?1 AND block_number=?2 ORDER BY sequence", s.key, b.Block.Number)
		if e != nil {
			return e
		}
		roots := []model.Root{}
		for rows.Next() {
			var r model.Root
			var h []byte
			if e = rows.Scan(&r.TreeID, &r.LeafCount, &h); e != nil {
				rows.Close()
				return e
			}
			r.Root = common.BytesToHash(h)
			r.Block = b.Block
			roots = append(roots, r)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, r := range roots {
			if e = s.ValidateRoot(ctx, r); e != nil {
				return e
			}
		}
	}
	gs, e := grants(b)
	if e != nil {
		return e
	}
	for _, v := range gs {
		g := v.Grant
		if _, e = tx.Exec(ctx, "INSERT INTO invisi_credit_grants VALUES(?1,?2,?3,?4,?5,?6,?7,?8)", s.key, g.Owner[:], g.Token[:], b.Block.Number, v.Event.LogIndex, v.Event.Name, string(g.Shard), g.Slot); e != nil {
			return e
		}
	}

	for _, n := range b.Nullifiers {
		if _, err = tx.Exec(ctx, "INSERT INTO invisi_nullifiers VALUES(?1,?2,?3)", s.key, n[:], b.Block.Number); err != nil {
			return err
		}
	}
	for _, n := range b.CreditNullifiers {
		if _, err = tx.Exec(ctx, "INSERT INTO invisi_credit_nullifiers VALUES(?1,?2,?3)", s.key, n[:], b.Block.Number); err != nil {
			return err
		}
	}
	return nil
}
func (s *SQLiteStore) insert(ctx context.Context, tx *sqliteTx, block, sequence uint64, in model.Insertion) error {
	if in.TreeID > math.MaxInt64 {
		return errors.New("tree ID out of range")
	}
	var count uint32
	if err := tx.QueryRow(ctx, "SELECT leaf_count FROM invisi_roots WHERE deployment=?1 AND tree_id=?2 ORDER BY leaf_count DESC LIMIT 1", s.key, in.TreeID).Scan(&count); err != nil {
		return fmt.Errorf("tree %d: %w", in.TreeID, err)
	}
	if in.Start != count {
		return errors.New("insertion start differs from tree leaf count")
	}
	if len(in.Commitments) == 0 {
		return errors.New("empty insertion")
	}
	if uint64(count)+uint64(len(in.Commitments)) > 1<<cryptography.TreeDepth {
		return errors.New("tree capacity exceeded")
	}
	changed := map[[2]uint32]common.Hash{}
	read := func(level uint8, index uint32) (common.Hash, bool, error) {
		if h, ok := changed[[2]uint32{uint32(level), index}]; ok {
			return h, true, nil
		}
		var h []byte
		e := tx.QueryRow(ctx, "SELECT hash FROM invisi_nodes WHERE deployment=?1 AND tree_id=?2 AND level=?3 AND node_index=?4 AND leaf_count<=?5 ORDER BY leaf_count DESC LIMIT 1", s.key, in.TreeID, level, index, count).Scan(&h)
		if errors.Is(e, sql.ErrNoRows) {
			return common.Hash{}, false, nil
		}
		return common.BytesToHash(h), e == nil, e
	}
	var root common.Hash
	for i, leaf := range in.Commitments {
		r, nodes, err := merkle.Insert(count+uint32(i), leaf, read)
		if err != nil {
			return err
		}
		root = r
		for _, node := range nodes {
			changed[[2]uint32{uint32(node.Level), node.Index}] = node.Hash
		}
	}
	final := count + uint32(len(in.Commitments))
	for k, h := range changed {
		if _, err := tx.Exec(ctx, "INSERT INTO invisi_nodes VALUES(?1,?2,?3,?4,?5,?6,?7)", s.key, in.TreeID, k[0], k[1], final, h[:], block); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, "INSERT INTO invisi_roots VALUES(?1,?2,?3,?4,?5,?6)", s.key, in.TreeID, final, root[:], block, sequence)
	return err
}
func (s *SQLiteStore) Rollback(ctx context.Context, ancestor uint64) error {
	if ancestor > math.MaxInt64 {
		return errors.New("ancestor out of range")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, "DELETE FROM invisi_blocks WHERE deployment=?1 AND number>?2", s.key, ancestor)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func sqliteValidLimit(limit int) error {
	if limit < 1 || limit > 10000 {
		return errors.New("limit must be between 1 and 10000")
	}
	return nil
}
func (s *SQLiteStore) Roots(ctx context.Context, treeID uint64, limit int) ([]model.Root, error) {
	if err := sqliteValidLimit(limit); err != nil {
		return nil, err
	}
	if treeID > math.MaxInt64 {
		return nil, errors.New("tree ID out of range")
	}
	rows, err := s.readPool.Query(ctx, "SELECT r.leaf_count,r.root,b.number,b.hash,b.parent_hash,b.batch->'block' FROM invisi_roots r JOIN invisi_blocks b ON b.deployment=r.deployment AND b.number=r.block_number WHERE r.deployment=?1 AND r.tree_id=?2 ORDER BY r.leaf_count DESC LIMIT ?3", s.key, treeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Root{}
	for rows.Next() {
		var r model.Root
		var root, h, p, blockJSON []byte
		r.TreeID = treeID
		if err = rows.Scan(&r.LeafCount, &root, &r.Block.Number, &h, &p, &blockJSON); err != nil {
			return nil, err
		}
		r.Root = common.BytesToHash(root)
		r.Block.Hash = common.BytesToHash(h)
		r.Block.ParentHash = common.BytesToHash(p)
		if err = json.Unmarshal(blockJSON, &r.Block); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *SQLiteStore) Membership(ctx context.Context, treeID uint64, index uint32, root common.Hash) (*model.Membership, error) {
	if treeID > math.MaxInt64 {
		return nil, errors.New("tree ID out of range")
	}
	tx, err := s.readPool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	m, err := s.membership(ctx, tx, treeID, index, root)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return m, nil
}
func (s *SQLiteStore) membership(ctx context.Context, tx sqliteQuerier, treeID uint64, index uint32, root common.Hash) (*model.Membership, error) {
	m := &model.Membership{LeafIndex: index}
	m.Root.TreeID = treeID
	var h, p, blockJSON []byte
	err := tx.QueryRow(ctx, "SELECT r.leaf_count,b.number,b.hash,b.parent_hash,b.batch->'block' FROM invisi_roots r JOIN invisi_blocks b ON b.deployment=r.deployment AND b.number=r.block_number WHERE r.deployment=?1 AND r.tree_id=?2 AND r.root=?3 ORDER BY r.leaf_count DESC LIMIT 1", s.key, treeID, root[:]).Scan(&m.Root.LeafCount, &m.Root.Block.Number, &h, &p, &blockJSON)
	if err != nil {
		return nil, err
	}
	m.Root.Root = root
	m.Root.Block.Hash = common.BytesToHash(h)
	m.Root.Block.ParentHash = common.BytesToHash(p)
	if err = json.Unmarshal(blockJSON, &m.Root.Block); err != nil {
		return nil, err
	}
	read := func(level uint8, i uint32) (common.Hash, bool, error) {
		var data []byte
		e := tx.QueryRow(ctx, "SELECT hash FROM invisi_nodes WHERE deployment=?1 AND tree_id=?2 AND level=?3 AND node_index=?4 AND leaf_count<=?5 ORDER BY leaf_count DESC LIMIT 1", s.key, treeID, level, i, m.Root.LeafCount).Scan(&data)
		if errors.Is(e, sql.ErrNoRows) {
			return common.Hash{}, false, nil
		}
		return common.BytesToHash(data), e == nil, e
	}
	siblings, err := merkle.Path(index, m.Root.LeafCount, read)
	if err != nil {
		return nil, err
	}
	m.Siblings = siblings[:]
	leaf, ok, err := read(0, index)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("missing commitment")
	}
	m.Commitment = leaf
	return m, nil
}
func (s *SQLiteStore) Events(ctx context.Context, from, to uint64, limit int) ([]model.Event, error) {
	raw, err := s.items(ctx, "events", from, to, limit)
	if err != nil {
		return nil, err
	}
	out := make([]model.Event, 0, len(raw))
	for _, r := range raw {
		var e model.Event
		if err = json.Unmarshal(r, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}
func (s *SQLiteStore) Outputs(ctx context.Context, from, to uint64, limit int) ([]model.NoteOutput, error) {
	raw, err := s.items(ctx, "outputs", from, to, limit)
	if err != nil {
		return nil, err
	}
	out := make([]model.NoteOutput, 0, len(raw))
	for _, r := range raw {
		var e model.NoteOutput
		if err = json.Unmarshal(r, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}
func (s *SQLiteStore) items(ctx context.Context, field string, from, to uint64, limit int) ([]json.RawMessage, error) {
	if err := sqliteValidLimit(limit); err != nil {
		return nil, err
	}
	if from > to || to > math.MaxInt64 {
		return nil, errors.New("invalid block range")
	}
	rows, err := s.readPool.Query(ctx, "SELECT x.value FROM invisi_blocks b, json_each(COALESCE(json_extract(b.batch, '$.' || ?2), '[]')) x WHERE b.deployment=?1 AND b.number BETWEEN ?3 AND ?4 ORDER BY b.number,CAST(x.key AS INTEGER) LIMIT ?5", s.key, field, from, to, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var r []byte
		if err = rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if len(out) > limit {
		return nil, ErrQueryLimit
	}
	return out, rows.Err()
}
func (s *SQLiteStore) IsSpent(ctx context.Context, hash common.Hash) (bool, error) {
	var spent bool
	err := s.readPool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM invisi_nullifiers WHERE deployment=?1 AND hash=?2)", s.key, hash[:]).Scan(&spent)
	return spent, err
}

// Import restores a complete stream atomically into an empty deployment. The
// immediate writer transaction excludes competing writers across process handles.
func (s *SQLiteStore) Import(ctx context.Context, next func() (model.Batch, error)) error {
	if next == nil {
		return errors.New("nil snapshot reader")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	head, err := s.checkpoint(ctx, tx)
	if err != nil {
		return err
	}
	if head != nil {
		return errors.New("snapshot import requires an empty deployment")
	}
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		b, e := next()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return e
		}
		if err = s.apply(ctx, tx, b); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Export streams bounded pages from one WAL read snapshot. Callbacks may write
// through another connection without altering the exported view or checkpoint.
func (s *SQLiteStore) Export(ctx context.Context, consume func(model.Batch) error) (*model.Block, error) {
	if consume == nil {
		return nil, errors.New("nil snapshot consumer")
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
	if head != nil {
		var last int64 = -1
		for {
			rows, e := tx.Query(ctx, "SELECT number,batch FROM invisi_blocks WHERE deployment=?1 AND number>?2 AND number<=?3 ORDER BY number LIMIT 128", s.key, last, head.Number)
			if e != nil {
				return nil, e
			}
			page := []model.Batch{}
			for rows.Next() {
				var number int64
				var data []byte
				if e = rows.Scan(&number, &data); e != nil {
					rows.Close()
					return nil, e
				}
				var b model.Batch
				if e = json.Unmarshal(data, &b); e != nil {
					rows.Close()
					return nil, e
				}
				page = append(page, b)
				last = number
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return nil, e
			}
			if len(page) == 0 {
				break
			}
			for _, b := range page {
				if e = ctx.Err(); e != nil {
					return nil, e
				}
				if e = consume(b); e != nil {
					return nil, e
				}
			}
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return head, nil
}

// ReceivingAddress reads the latest registration and indexed head in one database snapshot.
func (s *SQLiteStore) ReceivingAddress(ctx context.Context, owner common.Address) (*model.RegisteredReceivingAddress, error) {
	var blockRaw, registrationRaw []byte
	err := s.readPool.QueryRow(ctx, `SELECT head.block,r.registration FROM
 (SELECT json_extract(batch,'$.block') AS block FROM invisi_blocks WHERE deployment=?1 ORDER BY number DESC LIMIT 1) head
 LEFT JOIN invisi_registrations r ON r.deployment=?1 AND r.owner=?2
 ORDER BY r.block_number DESC,r.log_index DESC LIMIT 1`, s.key, owner[:]).Scan(&blockRaw, &registrationRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("registry index has not reached its first block")
	}
	if err != nil {
		return nil, err
	}
	var block model.Block
	if err = json.Unmarshal(blockRaw, &block); err != nil {
		return nil, err
	}
	if s.deployment.Registry == nil || block.Number < s.deployment.Registry.FromBlock {
		return nil, errors.New("registry index has not reached registry deployment")
	}
	if len(registrationRaw) == 0 {
		return nil, nil
	}
	var registration model.RegistrationUpdate
	if err = json.Unmarshal(registrationRaw, &registration); err != nil {
		return nil, err
	}
	return &model.RegisteredReceivingAddress{
		WalletIdentity:   registration.WalletIdentity,
		ViewingPublicKey: registration.ViewingPublicKey,
		ClueKey:          registration.ClueKey,
		Nonce:            registration.Nonce,
		Block:            block,
	}, nil
}

// Package storage maintains an atomic, deployment-scoped PostgreSQL index.
package storage

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/cryptography"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/merkle"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

// ErrQueryLimit asks callers to narrow their block range before retrying.
var ErrQueryLimit = errors.New("query result exceeds limit; narrow the block range")

type Store struct {
	ValidateRoot func(context.Context, model.Root) error
	pool         *pgxpool.Pool
	key          string
	deployment   model.Deployment
}

func Open(ctx context.Context, url string, deployment model.Deployment) (*Store, error) {
	if deployment.ChainID == "" {
		return nil, errors.New("chain ID is required")
	}
	if deployment.FromBlock > math.MaxInt64 {
		return nil, errors.New("from block exceeds PostgreSQL range")
	}
	p, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	s := &Store{pool: p, key: deployment.ChainID + ":" + strings.ToLower(deployment.Protocol.Hex()), deployment: deployment}
	// Serialize migrations across independently starting indexers.
	tx, err := p.Begin(ctx)
	if err != nil {
		p.Close()
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(721466908)"); err == nil {
		_, err = tx.Exec(ctx, schema)
	}
	if err == nil {
		err = s.lock(ctx, tx)
	}
	if err == nil {
		_, err = tx.Exec(ctx, "INSERT INTO invisi_deployments VALUES($1,$2,$3) ON CONFLICT DO NOTHING", s.key, deployment.FromBlock, deployment.RuntimeCodeHash[:])
	}
	if err == nil {
		var from uint64
		var hash []byte
		err = tx.QueryRow(ctx, "SELECT from_block,runtime_code_hash FROM invisi_deployments WHERE deployment=$1", s.key).Scan(&from, &hash)
		if err == nil && (from != deployment.FromBlock || common.BytesToHash(hash) != deployment.RuntimeCodeHash) {
			err = errors.New("deployment configuration differs from persisted configuration")
		}
	}
	if err == nil {
		profile, _ := json.Marshal(deployment.Registry)
		_, err = tx.Exec(ctx, "INSERT INTO invisi_registry_profiles VALUES($1,$2) ON CONFLICT DO NOTHING", s.key, string(profile))
		if err == nil {
			var saved string
			err = tx.QueryRow(ctx, "SELECT profile FROM invisi_registry_profiles WHERE deployment=$1", s.key).Scan(&saved)
			if err == nil && saved != string(profile) {
				err = errors.New("registry deployment differs from persisted configuration")
			}
		}
	}
	if err == nil {
		err = s.ensureBroadcasterProfile(ctx, tx)
	}
	if err == nil {
		_, err = tx.Exec(ctx, "INSERT INTO invisi_index_formats VALUES($1,2) ON CONFLICT DO NOTHING", s.key)
	}
	if err == nil {
		var version int
		err = tx.QueryRow(ctx, "SELECT version FROM invisi_index_formats WHERE deployment=$1", s.key).Scan(&version)
		if err == nil && version != 2 {
			err = errors.New("unsupported index format; use a fresh database and rebuild the event index")
		}
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		tx.Rollback(ctx)
		p.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() { s.pool.Close() }

type querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (s *Store) checkpoint(ctx context.Context, q querier) (*model.Block, error) {
	var b model.Block
	var raw []byte
	err := q.QueryRow(ctx, "SELECT batch->'block' FROM invisi_blocks WHERE deployment=$1 ORDER BY number DESC LIMIT 1", s.key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(raw, &b)
	return &b, err
}
func (s *Store) Checkpoint(ctx context.Context) (*model.Block, error) {
	return s.checkpoint(ctx, s.pool)
}

// Batch returns the complete persisted block and decoded/raw event material.
func (s *Store) Batch(ctx context.Context, number uint64) (*model.Batch, error) {
	if number > math.MaxInt64 {
		return nil, errors.New("block number out of range")
	}
	var data []byte
	err := s.pool.QueryRow(ctx, "SELECT batch FROM invisi_blocks WHERE deployment=$1 AND number=$2", s.key, number).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
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

func (s *Store) Block(ctx context.Context, number uint64) (*model.Block, error) {
	if number > math.MaxInt64 {
		return nil, errors.New("block number out of range")
	}
	var b model.Block
	var raw []byte
	err := s.pool.QueryRow(ctx, "SELECT batch->'block' FROM invisi_blocks WHERE deployment=$1 AND number=$2", s.key, number).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(raw, &b)
	return &b, err
}
func (s *Store) lock(ctx context.Context, tx pgx.Tx) error {
	_, e := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", s.key)
	return e
}
func (s *Store) SetRootValidator(validate func(context.Context, model.Root) error) {
	s.ValidateRoot = validate
}

// Apply atomically publishes one block after serializing deployment writers.
func (s *Store) Apply(ctx context.Context, b model.Batch) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = s.lock(ctx, tx); err != nil {
		return err
	}
	if err = s.apply(ctx, tx, b); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// apply runs inside the caller's deployment writer transaction.
func (s *Store) apply(ctx context.Context, tx pgx.Tx, b model.Batch) error {
	if b.Block.Number > math.MaxInt64 {
		return errors.New("block number out of range")
	}
	var old []byte
	err := tx.QueryRow(ctx, "SELECT hash FROM invisi_blocks WHERE deployment=$1 AND number=$2", s.key, b.Block.Number).Scan(&old)
	if err == nil {
		if common.BytesToHash(old) == b.Block.Hash {
			return nil
		}
		return errors.New("conflicting block hash")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
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
	if _, err = tx.Exec(ctx, "INSERT INTO invisi_blocks VALUES($1,$2,$3,$4,$5)", s.key, b.Block.Number, b.Block.Hash[:], b.Block.ParentHash[:], raw); err != nil {
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
		if _, err = tx.Exec(ctx, "INSERT INTO invisi_registrations VALUES($1,$2,$3,$4,$5)", s.key, registration.WalletIdentity[:], b.Block.Number, registration.LogIndex, raw); err != nil {
			return err
		}
	}
	for _, id := range b.CreatedTrees {
		if id > math.MaxInt64 {
			return errors.New("tree ID out of range")
		}
		if _, err = tx.Exec(ctx, "INSERT INTO invisi_trees VALUES($1,$2,$3)", s.key, id, b.Block.Number); err != nil {
			return err
		}
		root := cryptography.EmptyNodes[cryptography.TreeDepth]
		if _, err = tx.Exec(ctx, "INSERT INTO invisi_roots VALUES($1,$2,0,$3,$4,0)", s.key, id, root[:], b.Block.Number); err != nil {
			return err
		}
	}
	for sequence, in := range b.Insertions {
		if err = s.insert(ctx, tx, b.Block.Number, uint64(sequence+1), in); err != nil {
			return err
		}
	}
	if s.ValidateRoot != nil {
		rows, e := tx.Query(ctx, "SELECT tree_id,leaf_count,root FROM invisi_roots WHERE deployment=$1 AND block_number=$2 ORDER BY sequence", s.key, b.Block.Number)
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
	for _, n := range b.Nullifiers {
		if _, err = tx.Exec(ctx, "INSERT INTO invisi_nullifiers VALUES($1,$2,$3)", s.key, n[:], b.Block.Number); err != nil {
			return err
		}
	}
	return nil
}

// Import rebuilds all derived state in one transaction. The caller supplies
// verified, ordered batches and signals successful completion with io.EOF.
// Configure the root validator before import; offline snapshot restoration
// normally disables it and validates the snapshot's final chain anchor instead.
func (s *Store) Import(ctx context.Context, next func() (model.Batch, error)) error {
	if next == nil {
		return errors.New("import source is required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = s.lock(ctx, tx); err != nil {
		return err
	}
	head, err := s.checkpoint(ctx, tx)
	if err != nil {
		return err
	}
	if head != nil {
		return errors.New("import requires an empty deployment")
	}
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		batch, e := next()
		if errors.Is(e, io.EOF) {
			return tx.Commit(ctx)
		}
		if e != nil {
			return e
		}
		if e = s.apply(ctx, tx, batch); e != nil {
			return e
		}
	}
}

// Export streams batches and the corresponding checkpoint from one consistent
// database snapshot, even if a concurrent writer appends or rolls back blocks.
func (s *Store) Export(ctx context.Context, consume func(model.Batch) error) (*model.Block, error) {
	if consume == nil {
		return nil, errors.New("export consumer is required")
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
	rows, err := tx.Query(ctx, "SELECT batch FROM invisi_blocks WHERE deployment=$1 ORDER BY number", s.key)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, err
		}
		var batch model.Batch
		if err = json.Unmarshal(raw, &batch); err != nil {
			rows.Close()
			return nil, err
		}
		if err = consume(batch); err != nil {
			rows.Close()
			return nil, err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return head, nil
}

func (s *Store) insert(ctx context.Context, tx pgx.Tx, block, sequence uint64, in model.Insertion) error {
	if in.TreeID > math.MaxInt64 {
		return errors.New("tree ID out of range")
	}
	var count uint32
	if err := tx.QueryRow(ctx, "SELECT leaf_count FROM invisi_roots WHERE deployment=$1 AND tree_id=$2 ORDER BY leaf_count DESC LIMIT 1", s.key, in.TreeID).Scan(&count); err != nil {
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
		e := tx.QueryRow(ctx, "SELECT hash FROM invisi_nodes WHERE deployment=$1 AND tree_id=$2 AND level=$3 AND node_index=$4 AND leaf_count<=$5 ORDER BY leaf_count DESC LIMIT 1", s.key, in.TreeID, level, index, count).Scan(&h)
		if errors.Is(e, pgx.ErrNoRows) {
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
		if _, err := tx.Exec(ctx, "INSERT INTO invisi_nodes VALUES($1,$2,$3,$4,$5,$6,$7)", s.key, in.TreeID, k[0], k[1], final, h[:], block); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, "INSERT INTO invisi_roots VALUES($1,$2,$3,$4,$5,$6)", s.key, in.TreeID, final, root[:], block, sequence)
	return err
}
func (s *Store) Rollback(ctx context.Context, ancestor uint64) error {
	if ancestor > math.MaxInt64 {
		return errors.New("ancestor out of range")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = s.lock(ctx, tx); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "DELETE FROM invisi_blocks WHERE deployment=$1 AND number>$2", s.key, ancestor)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func validLimit(limit int) error {
	if limit < 1 || limit > 10000 {
		return errors.New("limit must be between 1 and 10000")
	}
	return nil
}
func (s *Store) Roots(ctx context.Context, treeID uint64, limit int) ([]model.Root, error) {
	if err := validLimit(limit); err != nil {
		return nil, err
	}
	if treeID > math.MaxInt64 {
		return nil, errors.New("tree ID out of range")
	}
	rows, err := s.pool.Query(ctx, "SELECT r.leaf_count,r.root,b.number,b.hash,b.parent_hash,b.batch->'block' FROM invisi_roots r JOIN invisi_blocks b ON b.deployment=r.deployment AND b.number=r.block_number WHERE r.deployment=$1 AND r.tree_id=$2 ORDER BY r.leaf_count DESC LIMIT $3", s.key, treeID, limit)
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
func (s *Store) Membership(ctx context.Context, treeID uint64, index uint32, root common.Hash) (*model.Membership, error) {
	if treeID > math.MaxInt64 {
		return nil, errors.New("tree ID out of range")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	m := &model.Membership{LeafIndex: index}
	m.Root.TreeID = treeID
	var h, p, blockJSON []byte
	err = tx.QueryRow(ctx, "SELECT r.leaf_count,b.number,b.hash,b.parent_hash,b.batch->'block' FROM invisi_roots r JOIN invisi_blocks b ON b.deployment=r.deployment AND b.number=r.block_number WHERE r.deployment=$1 AND r.tree_id=$2 AND r.root=$3 ORDER BY r.leaf_count DESC LIMIT 1", s.key, treeID, root[:]).Scan(&m.Root.LeafCount, &m.Root.Block.Number, &h, &p, &blockJSON)
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
		e := tx.QueryRow(ctx, "SELECT hash FROM invisi_nodes WHERE deployment=$1 AND tree_id=$2 AND level=$3 AND node_index=$4 AND leaf_count<=$5 ORDER BY leaf_count DESC LIMIT 1", s.key, treeID, level, i, m.Root.LeafCount).Scan(&data)
		if errors.Is(e, pgx.ErrNoRows) {
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
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return m, nil
}
func (s *Store) Events(ctx context.Context, from, to uint64, limit int) ([]model.Event, error) {
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
func (s *Store) Outputs(ctx context.Context, from, to uint64, limit int) ([]model.Output, error) {
	raw, err := s.items(ctx, "outputs", from, to, limit)
	if err != nil {
		return nil, err
	}
	out := make([]model.Output, 0, len(raw))
	for _, r := range raw {
		var e model.Output
		if err = json.Unmarshal(r, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}
func (s *Store) items(ctx context.Context, field string, from, to uint64, limit int) ([]json.RawMessage, error) {
	if err := validLimit(limit); err != nil {
		return nil, err
	}
	if from > to || to > math.MaxInt64 {
		return nil, errors.New("invalid block range")
	}
	rows, err := s.pool.Query(ctx, "SELECT item FROM invisi_blocks b CROSS JOIN LATERAL jsonb_array_elements(COALESCE(NULLIF(b.batch->$2,'null'::jsonb),'[]'::jsonb)) WITH ORDINALITY AS x(item,ordinal) WHERE deployment=$1 AND number BETWEEN $3 AND $4 ORDER BY number,ordinal LIMIT $5", s.key, field, from, to, limit+1)
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
func (s *Store) IsSpent(ctx context.Context, hash common.Hash) (bool, error) {
	var spent bool
	err := s.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM invisi_nullifiers WHERE deployment=$1 AND hash=$2)", s.key, hash[:]).Scan(&spent)
	return spent, err
}

// ReceivingAddress reads the latest registration and indexed head in one database snapshot.
func (s *Store) ReceivingAddress(ctx context.Context, owner common.Address) (*model.RegisteredReceivingAddress, error) {
	var blockRaw, registrationRaw []byte
	err := s.pool.QueryRow(ctx, `SELECT head.block,r.registration FROM
 (SELECT batch->'block' AS block FROM invisi_blocks WHERE deployment=$1 ORDER BY number DESC LIMIT 1) head
 LEFT JOIN invisi_registrations r ON r.deployment=$1 AND r.owner=$2
 ORDER BY r.block_number DESC,r.log_index DESC LIMIT 1`, s.key, owner[:]).Scan(&blockRaw, &registrationRaw)
	if errors.Is(err, pgx.ErrNoRows) {
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

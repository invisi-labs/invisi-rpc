package rpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-labs/invisi-rpc/internal/storage"
	"math"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invisi-labs/invisi-rpc/internal/model"
)

// Measurement-only adapter using the exact production read queries and four
// reader connections. It never executes schema setup or opens a writer; both
// URI mode=ro and a read-only volume mount enforce that on the live index.
type readonlyProfileStore struct {
	fakeBackend
	db  *sql.DB
	key string
}

func openReadonlyProfileStore(path string, d model.Deployment) (*readonlyProfileStore, error) {
	absolute, e := filepath.Abs(path)
	if e != nil {
		return nil, e
	}
	u := url.URL{Scheme: "file", Path: absolute}
	q := url.Values{"mode": {"ro"}, "_pragma": {"query_only(1)", "busy_timeout(5000)"}}
	u.RawQuery = q.Encode()
	db, e := sql.Open("sqlite", u.String())
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(4)
	return &readonlyProfileStore{db: db, key: d.ChainID + ":" + strings.ToLower(d.Protocol.Hex())}, nil
}
func (s *readonlyProfileStore) Checkpoint(ctx context.Context) (*model.Block, error) {
	var raw []byte
	e := s.db.QueryRowContext(ctx, "SELECT batch->'block' FROM invisi_blocks WHERE deployment=?1 ORDER BY number DESC LIMIT 1", s.key).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var b model.Block
	e = json.Unmarshal(raw, &b)
	return &b, e
}
func (s *readonlyProfileStore) Block(ctx context.Context, n uint64) (*model.Block, error) {
	if n > math.MaxInt64 {
		return nil, errors.New("block number out of range")
	}
	var raw []byte
	e := s.db.QueryRowContext(ctx, "SELECT batch->'block' FROM invisi_blocks WHERE deployment=?1 AND number=?2", s.key, n).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var b model.Block
	e = json.Unmarshal(raw, &b)
	return &b, e
}
func (s *readonlyProfileStore) Batch(ctx context.Context, n uint64) (*model.Batch, error) {
	if n > math.MaxInt64 {
		return nil, errors.New("block number out of range")
	}
	var raw []byte
	e := s.db.QueryRowContext(ctx, "SELECT batch FROM invisi_blocks WHERE deployment=?1 AND number=?2", s.key, n).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var b model.Batch
	e = json.Unmarshal(raw, &b)
	return &b, e
}

func TestReadonlyProfileStoreMatchesProductionQueries(t *testing.T) {
	ctx := context.Background()
	d := model.Deployment{ChainID: "97", Protocol: common.HexToAddress("0x1"), FromBlock: 10}
	path := filepath.Join(t.TempDir(), "rpc.sqlite")
	source, e := storage.OpenSQLite(ctx, path, d)
	if e != nil {
		t.Fatal(e)
	}
	defer source.Close()
	batch := model.Batch{Block: model.Block{Number: 10, Hash: common.HexToHash("0x1234"), Timestamp: 100}}
	if e = source.Apply(ctx, batch); e != nil {
		t.Fatal(e)
	}
	reader, e := openReadonlyProfileStore(path, d)
	if e != nil {
		t.Fatal(e)
	}
	defer reader.db.Close()
	got, e := reader.Batch(ctx, 10)
	if e != nil {
		t.Fatal(e)
	}
	want, e := source.Batch(ctx, 10)
	if e != nil {
		t.Fatal(e)
	}
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(want)
	if string(a) != string(b) {
		t.Fatal("profile query differs from production")
	}
	head, e := reader.Checkpoint(ctx)
	if e != nil || head == nil || head.Number != 10 {
		t.Fatal("profile checkpoint unavailable", e)
	}
	block, e := reader.Block(ctx, 10)
	if e != nil || block == nil || block.Hash != batch.Block.Hash {
		t.Fatal("profile boundary unavailable", e)
	}
	if _, e = reader.db.ExecContext(ctx, "CREATE TABLE profile_must_not_write(id INTEGER)"); e == nil {
		t.Fatal("profile connection permits writes")
	}
}

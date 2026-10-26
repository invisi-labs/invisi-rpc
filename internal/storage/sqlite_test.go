package storage

import (
	"context"
	"errors"
	"fmt"
	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/cryptography"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
	"io"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteAtomicHistoryRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	ctx := context.Background()
	dep := model.Deployment{ChainID: fmt.Sprintf("storage-test-%d", time.Now().UnixNano()), FromBlock: 10}
	s, err := OpenSQLite(ctx, path, dep)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	apply := func(b model.Batch) {
		t.Helper()
		if err := s.Apply(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	b10 := model.Batch{Block: model.Block{Number: 10, Hash: common.HexToHash("0x10")}, CreatedTrees: []uint64{1}}
	apply(b10)
	apply(b10)
	leaf1, leaf2 := common.HexToHash("0x01"), common.HexToHash("0x02")
	b11 := model.Batch{Block: model.Block{Number: 11, Hash: common.HexToHash("0x11"), ParentHash: b10.Block.Hash}, Insertions: []model.Insertion{{TreeID: 1, Commitments: []common.Hash{leaf1}}}, Events: []model.Event{{Name: "insert"}}, Outputs: []model.Output{{Commitment: leaf1}}, Nullifiers: []common.Hash{leaf2}}
	apply(b11)
	persisted, err := s.Batch(ctx, 11)
	if err != nil || persisted == nil || len(persisted.Outputs) != 1 || len(persisted.Events) != 1 {
		t.Fatalf("batch roundtrip: %v %v", persisted, err)
	}
	mismatch := dep
	mismatch.FromBlock++
	if invalid, e := OpenSQLite(ctx, path, mismatch); e == nil {
		invalid.Close()
		t.Fatal("accepted mismatched deployment start")
	}
	mismatch = dep
	mismatch.RuntimeCodeHash = leaf1
	if invalid, e := OpenSQLite(ctx, path, mismatch); e == nil {
		invalid.Close()
		t.Fatal("accepted mismatched runtime hash")
	}
	roots, err := s.Roots(ctx, 1, 10)
	if err != nil || len(roots) != 2 {
		t.Fatalf("roots: %v %v", roots, err)
	}
	first := roots[0].Root
	if roots[1].Root != cryptography.EmptyNodes[cryptography.TreeDepth] {
		t.Fatal("incorrect empty root")
	}
	b12 := model.Batch{Block: model.Block{Number: 12, Hash: common.HexToHash("0x12"), ParentHash: b11.Block.Hash}, Insertions: []model.Insertion{{TreeID: 1, Start: 1, Commitments: []common.Hash{leaf2}}}}
	b12.Events = []model.Event{{Name: "second"}}
	b12.Outputs = []model.Output{{Commitment: leaf2}}
	apply(b12)
	if _, e := s.Events(ctx, 10, 12, 1); !errors.Is(e, ErrQueryLimit) {
		t.Fatalf("event overflow: %v", e)
	}
	if _, e := s.Outputs(ctx, 10, 12, 1); !errors.Is(e, ErrQueryLimit) {
		t.Fatalf("output overflow: %v", e)
	}
	membership, err := s.Membership(ctx, 1, 0, first)
	if err != nil {
		t.Fatal(err)
	}
	if membership.Commitment != leaf1 || membership.Siblings[0] != cryptography.EmptyNodes[0] {
		t.Fatal("historical path changed")
	}
	events, err := s.Events(ctx, 10, 12, 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("events: %v %v", events, err)
	}
	outputs, err := s.Outputs(ctx, 10, 12, 10)
	if err != nil || len(outputs) != 2 {
		t.Fatalf("outputs: %v %v", outputs, err)
	}
	if ok, err := s.IsSpent(ctx, leaf2); err != nil || !ok {
		t.Fatalf("nullifier: %v %v", ok, err)
	}
	if err = s.Rollback(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.IsSpent(ctx, leaf2); err != nil || ok {
		t.Fatalf("rollback nullifier: %v %v", ok, err)
	}
	roots, err = s.Roots(ctx, 1, 10)
	if err != nil || len(roots) != 1 {
		t.Fatalf("rollback roots: %v %v", roots, err)
	}
	s.ValidateRoot = func(context.Context, model.Root) error { return errors.New("chain root mismatch") }
	if err = s.Apply(ctx, b11); err == nil {
		t.Fatal("root validation should reject transaction")
	}
	head, err := s.Checkpoint(ctx)
	if err != nil || head.Number != 10 {
		t.Fatalf("atomic head: %v %v", head, err)
	}
	s.ValidateRoot = nil
	apply(b11)
	bad := b12
	bad.Block.ParentHash = common.Hash{}
	if err = s.Apply(ctx, bad); err == nil {
		t.Fatal("bad parent accepted")
	}
	other, err := OpenSQLite(ctx, path, model.Deployment{ChainID: dep.ChainID, Protocol: common.HexToAddress("0x1"), FromBlock: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if head, err := other.Checkpoint(ctx); err != nil || head != nil {
		t.Fatalf("deployment isolation: %v %v", head, err)
	}
}

func sqliteFixture(t *testing.T) (*SQLiteStore, model.Deployment, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index.sqlite")
	dep := model.Deployment{ChainID: "31337", FromBlock: 10}
	s, e := OpenSQLite(context.Background(), path, dep)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	return s, dep, path
}
func sqliteStream(batches []model.Batch, tail error) func() (model.Batch, error) {
	i := 0
	return func() (model.Batch, error) {
		if i == len(batches) {
			return model.Batch{}, tail
		}
		b := batches[i]
		i++
		return b, nil
	}
}
func TestSQLiteImportAtomicAndSnapshotIsolation(t *testing.T) {
	ctx := context.Background()
	s, dep, path := sqliteFixture(t)
	first := model.Batch{Block: model.Block{Number: 10, Hash: common.HexToHash("0x10")}, CreatedTrees: []uint64{1}}
	second := model.Batch{Block: model.Block{Number: 11, Hash: common.HexToHash("0x11"), ParentHash: first.Block.Hash}, Insertions: []model.Insertion{{TreeID: 1, Commitments: []common.Hash{common.HexToHash("0x01")}}}}
	bad := second
	bad.Block.ParentHash = common.Hash{}
	if err := s.Import(ctx, sqliteStream([]model.Batch{first, bad}, io.EOF)); err == nil {
		t.Fatal("bad snapshot accepted")
	}
	if cp, e := s.Checkpoint(ctx); e != nil || cp != nil {
		t.Fatalf("partial import escaped: %v %v", cp, e)
	}
	sentinel := errors.New("broken input")
	if err := s.Import(ctx, sqliteStream([]model.Batch{first}, sentinel)); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if cp, _ := s.Checkpoint(ctx); cp != nil {
		t.Fatal("stream error published")
	}
	if err := s.Import(ctx, sqliteStream([]model.Batch{first}, io.EOF)); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := s.Import(ctx, func() (model.Batch, error) { called = true; return model.Batch{}, io.EOF }); err == nil || called {
		t.Fatal("nonempty import accepted or consumed")
	}
	other, e := OpenSQLite(ctx, path, dep)
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	var exported []model.Batch
	cp, e := s.Export(ctx, func(b model.Batch) error { exported = append(exported, b); return other.Apply(ctx, second) })
	if e != nil || cp.Number != 10 || len(exported) != 1 {
		t.Fatalf("snapshot drift: %+v %v %d", cp, e, len(exported))
	}
	if latest, e := s.Checkpoint(ctx); e != nil || latest.Number != 11 {
		t.Fatalf("concurrent write failed: %+v %v", latest, e)
	}
	s.Close()
	reopened, e := OpenSQLite(ctx, path, dep)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	if latest, e := reopened.Checkpoint(ctx); e != nil || latest.Number != 11 {
		t.Fatal("restart lost checkpoint")
	}
}
func TestSQLiteOptionsAndCancelledImport(t *testing.T) {
	ctx := context.Background()
	s, _, _ := sqliteFixture(t)
	var journal string
	var foreign int
	if e := s.readPool.QueryRow(ctx, "PRAGMA journal_mode").Scan(&journal); e != nil || journal != "wal" {
		t.Fatalf("WAL: %q %v", journal, e)
	}
	if e := s.readPool.QueryRow(ctx, "PRAGMA foreign_keys").Scan(&foreign); e != nil || foreign != 1 {
		t.Fatalf("foreign keys: %d %v", foreign, e)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if e := s.Import(cancelled, sqliteStream(nil, io.EOF)); e == nil {
		t.Fatal("cancelled import succeeded")
	}
	if _, e := OpenSQLite(ctx, ":memory:", model.Deployment{ChainID: "1"}); e == nil {
		t.Fatal("nonpersistent database accepted")
	}
}

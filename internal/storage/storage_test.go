package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/cryptography"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
)

func TestPostgresAtomicHistoryRollback(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	dep := model.Deployment{ChainID: fmt.Sprintf("storage-test-%d", time.Now().UnixNano()), FromBlock: 10}
	s, err := Open(ctx, url, dep)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	defer s.pool.Exec(ctx, "DELETE FROM invisi_deployments WHERE deployment=$1", s.key)
	defer s.pool.Exec(ctx, "DELETE FROM invisi_blocks WHERE deployment=$1", s.key)
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
	if invalid, e := Open(ctx, url, mismatch); e == nil {
		invalid.Close()
		t.Fatal("accepted mismatched deployment start")
	}
	mismatch = dep
	mismatch.RuntimeCodeHash = leaf1
	if invalid, e := Open(ctx, url, mismatch); e == nil {
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
	other, err := Open(ctx, url, model.Deployment{ChainID: dep.ChainID, Protocol: common.HexToAddress("0x1"), FromBlock: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if head, err := other.Checkpoint(ctx); err != nil || head != nil {
		t.Fatalf("deployment isolation: %v %v", head, err)
	}
}

func snapshotStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	dep := model.Deployment{ChainID: fmt.Sprintf("snapshot-test-%d", time.Now().UnixNano()), FromBlock: 20}
	s, err := Open(ctx, url, dep)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer s.Close()
		if _, e := s.pool.Exec(ctx, "DELETE FROM invisi_blocks WHERE deployment=$1", s.key); e != nil {
			t.Error(e)
		}
		if _, e := s.pool.Exec(ctx, "DELETE FROM invisi_deployments WHERE deployment=$1", s.key); e != nil {
			t.Error(e)
		}
	})
	return s, ctx
}
func snapshotBatches() []model.Batch {
	first := model.Batch{Block: model.Block{Number: 20, Hash: common.HexToHash("0x20")}, CreatedTrees: []uint64{1}, Insertions: []model.Insertion{{TreeID: 1, Commitments: []common.Hash{common.HexToHash("0x1")}}}}
	second := model.Batch{Block: model.Block{Number: 21, Hash: common.HexToHash("0x21"), ParentHash: first.Block.Hash}, Insertions: []model.Insertion{{TreeID: 1, Start: 1, Commitments: []common.Hash{common.HexToHash("0x2")}}}, Nullifiers: []common.Hash{common.HexToHash("0xabc")}}
	return []model.Batch{first, second}
}
func TestPostgresImportAtomicity(t *testing.T) {
	s, ctx := snapshotStore(t)
	batches := snapshotBatches()
	streamFailure := errors.New("invalid snapshot footer")
	assertEmpty := func() {
		t.Helper()
		head, err := s.Checkpoint(ctx)
		if err != nil || head != nil {
			t.Fatalf("partial checkpoint: %v %v", head, err)
		}
		for _, table := range []string{"invisi_blocks", "invisi_trees", "invisi_roots", "invisi_nodes", "invisi_nullifiers"} {
			var count int
			err = s.pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE deployment=$1", s.key).Scan(&count)
			if err != nil || count != 0 {
				t.Fatalf("partial %s state: %d %v", table, count, err)
			}
		}
	}
	position := 0
	err := s.Import(ctx, func() (model.Batch, error) {
		if position < len(batches) {
			b := batches[position]
			position++
			return b, nil
		}
		return model.Batch{}, streamFailure
	})
	if !errors.Is(err, streamFailure) {
		t.Fatalf("source error: %v", err)
	}
	assertEmpty()
	position = 0
	invalid := batches[1]
	invalid.Block.ParentHash = common.Hash{}
	err = s.Import(ctx, func() (model.Batch, error) {
		position++
		if position == 1 {
			return batches[0], nil
		}
		return invalid, nil
	})
	if err == nil {
		t.Fatal("invalid middle block accepted")
	}
	assertEmpty()
	position = 0
	err = s.Import(ctx, func() (model.Batch, error) {
		if position == len(batches) {
			return model.Batch{}, io.EOF
		}
		b := batches[position]
		position++
		return b, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	roots, err := s.Roots(ctx, 1, 10)
	if err != nil || len(roots) != 3 {
		t.Fatalf("import roots: %v %v", roots, err)
	}
	old, err := s.Membership(ctx, 1, 0, roots[1].Root)
	if err != nil || old.Commitment != batches[0].Insertions[0].Commitments[0] {
		t.Fatalf("import history: %v %v", old, err)
	}
	called := false
	err = s.Import(ctx, func() (model.Batch, error) { called = true; return model.Batch{}, io.EOF })
	if err == nil || called {
		t.Fatalf("nonempty import accepted or consumed source: %v %v", called, err)
	}
}
func TestPostgresExportConsistentWithConcurrentAppend(t *testing.T) {
	s, ctx := snapshotStore(t)
	batches := snapshotBatches()
	for _, b := range batches {
		if err := s.Apply(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	third := model.Batch{Block: model.Block{Number: 22, Hash: common.HexToHash("0x22"), ParentHash: batches[1].Block.Hash}}
	got := []model.Batch{}
	head, err := s.Export(ctx, func(b model.Batch) error {
		got = append(got, b)
		if len(got) == 1 {
			return s.Apply(ctx, third)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if head == nil || head.Number != 21 || len(got) != 2 || got[1].Block.Hash != head.Hash {
		t.Fatalf("inconsistent snapshot: %v %+v", head, got)
	}
	live, err := s.Checkpoint(ctx)
	if err != nil || live.Number != 22 {
		t.Fatalf("writer did not append: %v %v", live, err)
	}
	failure := errors.New("disk full")
	_, err = s.Export(ctx, func(model.Batch) error { return failure })
	if !errors.Is(err, failure) {
		t.Fatalf("consumer error: %v", err)
	}
}

func TestPostgresCreditBlockContextAndFormatValidation(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	dep := model.Deployment{ChainID: fmt.Sprintf("credit-%d", time.Now().UnixNano()), FromBlock: 1}
	s, err := Open(ctx, url, dep)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	defer s.pool.Exec(ctx, "DELETE FROM invisi_index_formats WHERE deployment=$1", s.key)
	defer s.pool.Exec(ctx, "DELETE FROM invisi_deployments WHERE deployment=$1", s.key)
	defer s.pool.Exec(ctx, "DELETE FROM invisi_blocks WHERE deployment=$1", s.key)
	b := model.Batch{Block: model.Block{Number: 1, Hash: common.HexToHash("1"), Timestamp: 2678400}, CreatedTrees: []uint64{1}, Events: []model.Event{{Name: "CreditDraw"}}, Outputs: []model.Output{{Kind: "CreditDraw"}}}
	if err = s.Apply(ctx, b); err != nil {
		t.Fatal(err)
	}
	cp, err := s.Checkpoint(ctx)
	if err != nil || *cp != b.Block {
		t.Fatalf("checkpoint %v %v", cp, err)
	}
	events, err := s.Events(ctx, 1, 1, 10)
	if err != nil || len(events) != 1 || events[0].Block != b.Block {
		t.Fatalf("events %v %v", events, err)
	}
	outputs, err := s.Outputs(ctx, 1, 1, 10)
	if err != nil || len(outputs) != 1 || outputs[0].Block != b.Block {
		t.Fatalf("outputs %v %v", outputs, err)
	}
	roots, err := s.Roots(ctx, 1, 10)
	if err != nil || len(roots) != 1 || roots[0].Block != b.Block {
		t.Fatalf("roots %v %v", roots, err)
	}
	if _, err = s.pool.Exec(ctx, "UPDATE invisi_blocks SET batch=batch #- '{block,timestamp}' WHERE deployment=$1", s.key); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Checkpoint(ctx); err == nil {
		t.Fatal("accepted current block record missing timestamp")
	}
	if _, err = s.pool.Exec(ctx, "UPDATE invisi_index_formats SET version=99 WHERE deployment=$1", s.key); err != nil {
		t.Fatal(err)
	}
	if invalid, e := Open(ctx, url, dep); e == nil {
		invalid.Close()
		t.Fatal("accepted corrupt current index format")
	} else if !strings.Contains(e.Error(), "unsupported index format") {
		t.Fatal(e)
	}
}

func TestPostgresRegistryHistory(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	dep := model.Deployment{ChainID: fmt.Sprintf("registry-%d", time.Now().UnixNano()), Protocol: common.HexToAddress("0x1"), FromBlock: 10, Registry: &model.ContractDeployment{Address: common.HexToAddress("0x2"), RuntimeCodeHash: common.HexToHash("0x3"), FromBlock: 8}}
	s, err := Open(ctx, url, dep)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	defer s.pool.Exec(ctx, "DELETE FROM invisi_blocks WHERE deployment=$1", s.key)
	owner := common.HexToAddress("0x1234")
	for n := uint64(8); n <= 10; n++ {
		b := model.Batch{Block: model.Block{Number: n, Hash: common.BigToHash(new(big.Int).SetUint64(n)), ParentHash: common.BigToHash(new(big.Int).SetUint64(n - 1))}}
		if n == 8 || n == 10 {
			b.Registrations = []model.RegistrationUpdate{{WalletIdentity: owner, ViewingPublicKey: []byte{2}, ClueKey: common.HexToHash("0x1"), Nonce: n, LogIndex: 1}}
		}
		if n == 10 {
			b.Registrations = append(b.Registrations, model.RegistrationUpdate{WalletIdentity: owner, ViewingPublicKey: []byte{3}, ClueKey: common.HexToHash("0x2"), Nonce: 18446744073709551615, LogIndex: 2})
		}
		if err = s.Apply(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ReceivingAddress(ctx, owner)
	if err != nil || got == nil || got.Nonce != 18446744073709551615 {
		t.Fatalf("latest: %+v %v", got, err)
	}
	if err = s.Rollback(ctx, 9); err != nil {
		t.Fatal(err)
	}
	got, err = s.ReceivingAddress(ctx, owner)
	if err != nil || got == nil || got.Nonce != 8 {
		t.Fatalf("rollback: %+v %v", got, err)
	}
	got, err = s.ReceivingAddress(ctx, common.HexToAddress("0x5678"))
	if err != nil || got != nil {
		t.Fatalf("unregistered: %+v %v", got, err)
	}
}

package snapshot

import (
	"context"
	"math/big"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	"github.com/invisi-labs/invisi-rpc/internal/storage"
)

func TestCompleteHeaderSnapshotRoundTripAndIntegrity(t *testing.T) {
	ctx := context.Background()
	dep, _ := fixture()
	first := &types.Header{Number: big.NewInt(10), Difficulty: big.NewInt(1), Extra: []byte("original full header"), Time: 100}
	second := &types.Header{Number: big.NewInt(11), Difficulty: big.NewInt(1), ParentHash: first.Hash(), Time: 101}
	batches := []model.Batch{}
	for _, h := range []*types.Header{first, second} {
		batches = append(batches, model.Batch{Header: h, Block: model.Block{Number: h.Number.Uint64(), Hash: h.Hash(), ParentHash: h.ParentHash, Timestamp: h.Time}})
	}
	source, e := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "source.sqlite"), dep)
	if e != nil {
		t.Fatal(e)
	}
	defer source.Close()
	for _, b := range batches {
		if e = source.Apply(ctx, b); e != nil {
			t.Fatal(e)
		}
	}
	path, e := Export(ctx, source, dep, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	target, e := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "target.sqlite"), dep)
	if e != nil {
		t.Fatal(e)
	}
	defer target.Close()
	if e = Restore(ctx, target, dep, path, fileHash(t, path)); e != nil {
		t.Fatal(e)
	}
	rows, e := target.Headers(ctx, 10, 11)
	if e != nil || len(rows) != 2 || rows[0].Header.Hash() != first.Hash() || rows[1].Header.Hash() != second.Hash() {
		t.Fatal("snapshot lost full headers", e)
	}
	// Present but inconsistent headers are rejected even on snapshot inspection.
	bad := batches[1]
	bad.Header = types.CopyHeader(second)
	bad.Header.Extra = []byte("corrupt")
	h := header(dep)
	badPath := filepath.Join(t.TempDir(), "bad.snapshot.gz")
	writeRecords(t, badPath, []record{{Type: "header", Header: &h}, {Type: "block", Batch: &batches[0]}, {Type: "block", Batch: &bad}, {Type: "footer", Checkpoint: &bad.Block}})
	if _, e = Inspect(badPath); e == nil {
		t.Fatal("accepted corrupt snapshot header")
	}
}

package storage

import (
	"context"
	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyIndexUpgradeRefusesWithoutDeletingData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	dep := model.Deployment{ChainID: "1", FromBlock: 10}
	s, e := OpenSQLite(ctx, path, dep)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.Apply(ctx, model.Batch{Block: model.Block{Number: 10, Hash: common.HexToHash("10")}}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.pool.ExecContext(ctx, "UPDATE invisi_index_formats SET version=3"); e != nil {
		t.Fatal(e)
	}
	if other, e := OpenSQLite(ctx, path, dep); e == nil {
		other.Close()
		t.Fatal("accepted legacy index")
	} else if !strings.Contains(e.Error(), "separate database") {
		t.Fatal(e)
	}
	if b, e := s.Checkpoint(ctx); e != nil || b == nil || b.Number != 10 {
		t.Fatalf("legacy data lost %v %v", b, e)
	}
}

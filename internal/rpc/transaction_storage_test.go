package rpc

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/storage"
)

func TestTransactionUpdatesSQLiteRollback(t *testing.T) {
	api, fake, _, hash := transactionFixture(t)
	ctx := context.Background()
	// Start at the transaction's block so the fixture represents a contiguous index.
	api.Deployment.FromBlock = 10
	store, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "index.sqlite"), api.Deployment)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	batch := *fake.batch
	batch.Outputs = []model.NoteOutput{{TransactionHash: hash, LogIndex: 1}}
	batch.Events = []model.Event{{TransactionHash: hash, Name: "CreditDraw", LogIndex: 2, Data: []byte(`{}`)}}
	if err = store.Apply(ctx, batch); err != nil {
		t.Fatal(err)
	}
	api.Store = store
	result, err := api.transactionUpdates(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "indexed" || len(result.Outputs) != 1 || len(result.CreditTransitions) != 1 {
		t.Fatalf("unexpected persisted updates: %+v", result)
	}
	if err = store.Rollback(ctx, 9); err != nil {
		t.Fatal(err)
	}
	result, err = api.transactionUpdates(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "pending_index" || result.Outputs != nil {
		t.Fatalf("served rolled back updates: %+v", result)
	}
}

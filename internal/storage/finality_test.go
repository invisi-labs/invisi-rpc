package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-rpc/internal/model"
)

func TestSQLiteFinalizedAnchorSurvivesRestartAndRollback(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "anchor.sqlite")
	dep := model.Deployment{ChainID: "31337", FromBlock: 10}
	s, err := OpenSQLite(ctx, path, dep)
	if err != nil {
		t.Fatal(err)
	}
	anchor := model.Block{Number: 20, Timestamp: 123, Hash: common.HexToHash("0x1")}
	if err = s.SaveFinalizedAnchor(ctx, anchor); err != nil {
		t.Fatal(err)
	}
	if err = s.Rollback(ctx, 10); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = OpenSQLite(ctx, path, dep)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	got, err := s.FinalizedAnchor(ctx)
	if err != nil || got == nil || *got != anchor {
		t.Fatal(got, err)
	}
	for _, b := range []model.Block{{Number: 19}, {Number: 20, Timestamp: 123, Hash: common.HexToHash("0x2")}} {
		if err = s.SaveFinalizedAnchor(ctx, b); err == nil {
			t.Fatal("accepted anchor conflict")
		}
	}
	if err = s.SaveFinalizedAnchor(ctx, anchor); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkFinalityConflict(ctx); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = OpenSQLite(ctx, path, dep)
	if err != nil {
		t.Fatal(err)
	}
	fenced, err := s.FinalityConflict(ctx)
	if err != nil || !fenced {
		t.Fatal(fenced, err)
	}
	if err = s.SaveFinalizedAnchor(ctx, model.Block{Number: 21}); err == nil {
		t.Fatal("advanced fenced anchor")
	}
}

func TestPostgresFinalizedAnchorSurvivesRestartAndRollback(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	dep := model.Deployment{ChainID: fmt.Sprintf("finality-%d", time.Now().UnixNano()), FromBlock: 10}
	s, err := OpenPostgres(ctx, url, dep)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.pool.Exec(ctx, "DELETE FROM invisi_finalized_anchors WHERE deployment=$1", s.key); s.Close() }()
	anchor := model.Block{Number: 20, Timestamp: 123, Hash: common.HexToHash("0x1")}
	if err = s.SaveFinalizedAnchor(ctx, anchor); err != nil {
		t.Fatal(err)
	}
	if err = s.Rollback(ctx, 10); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = OpenPostgres(ctx, url, dep)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.FinalizedAnchor(ctx)
	if err != nil || got == nil || *got != anchor {
		t.Fatal(got, err)
	}
	if err = s.SaveFinalizedAnchor(ctx, model.Block{Number: 19}); err == nil {
		t.Fatal("accepted regression")
	}
	if err = s.MarkFinalityConflict(ctx); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = OpenPostgres(ctx, url, dep)
	if err != nil {
		t.Fatal(err)
	}
	fenced, err := s.FinalityConflict(ctx)
	if err != nil || !fenced {
		t.Fatal(fenced, err)
	}
	if err = s.SaveFinalizedAnchor(ctx, model.Block{Number: 21}); err == nil {
		t.Fatal("advanced fenced anchor")
	}
}

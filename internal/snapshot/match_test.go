package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/storage"
)

func matchSQLite(t *testing.T, dep model.Deployment, batches []model.Batch) *storage.SQLiteStore {
	t.Helper()
	s, e := storage.OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "match.sqlite"), dep)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	for _, b := range batches {
		if e = s.Apply(context.Background(), b); e != nil {
			t.Fatal(e)
		}
	}
	return s
}
func matchSource(t *testing.T, dep model.Deployment, batches []model.Batch) string {
	t.Helper()
	last := batches[len(batches)-1].Block
	p, e := Export(context.Background(), &fakeStore{batches: batches, head: &last}, dep, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func TestMatchesSQLiteSemanticBatchJSON(t *testing.T) {
	dep, batches := fixture()
	batches[1].Events = []model.Event{{Name: "large", Data: json.RawMessage(`{"z":9007199254740993,"a":{"second":2,"first":1}}`)}}
	path := matchSource(t, dep, batches)
	persisted := append([]model.Batch(nil), batches...)
	persisted[1].Events = []model.Event{{Name: "large", Data: json.RawMessage(` { "a": {"first": 1, "second": 2}, "z": 9007199254740993 } `)}}
	s := matchSQLite(t, dep, persisted)
	for _, digest := range []string{"", fileHash(t, path)} {
		ok, e := Matches(context.Background(), s, dep, path, digest, DefaultMaxDecompressedBytes)
		if e != nil || !ok {
			t.Fatalf("semantic match %v %v", ok, e)
		}
	}
	before, e := s.Checkpoint(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	// A valid source with different material is a mismatch, not a format failure.
	changed := append([]model.Batch(nil), batches...)
	changed[1].Events = []model.Event{{Name: "large", Data: json.RawMessage(`{"z":9007199254740992,"a":{"second":2,"first":1}}`)}}
	changedPath := matchSource(t, dep, changed)
	if ok, e := Matches(context.Background(), s, dep, changedPath, fileHash(t, changedPath), DefaultMaxDecompressedBytes); e != nil || ok {
		t.Fatalf("modified source accepted %v %v", ok, e)
	}
	after, e := s.Checkpoint(context.Background())
	if e != nil || *before != *after {
		t.Fatal("read-only comparison changed checkpoint")
	}
}
func TestMatchesSQLiteCountsAndPersistedDifferences(t *testing.T) {
	dep, batches := fixture()
	path := matchSource(t, dep, batches)
	for _, kind := range []string{"empty", "short", "extra", "different"} {
		t.Run(kind, func(t *testing.T) {
			saved := append([]model.Batch(nil), batches...)
			switch kind {
			case "empty":
				saved = nil
			case "short":
				saved = saved[:1]
			case "extra":
				saved = append(saved, model.Batch{Block: model.Block{Number: 12, Hash: common.HexToHash("12"), ParentHash: batches[1].Block.Hash}})
			case "different":
				saved[1].Events = []model.Event{{Name: "foreign"}}
			}
			s := matchSQLite(t, dep, saved)
			if ok, e := Matches(context.Background(), s, dep, path, fileHash(t, path), DefaultMaxDecompressedBytes); e != nil || ok {
				t.Fatalf("mismatched database %v %v", ok, e)
			}
		})
	}
}
func TestMatchesValidatesEntireSourceOnMismatch(t *testing.T) {
	dep, batches := fixture()
	s := matchSQLite(t, dep, nil)
	path := matchSource(t, dep, batches)
	for _, digest := range []string{"invalid", strings.Repeat("00", 31), strings.Repeat("00", 32)} {
		if ok, e := Matches(context.Background(), s, dep, path, digest, DefaultMaxDecompressedBytes); e == nil || ok {
			t.Fatalf("invalid checksum accepted %v %v", ok, e)
		}
	}
	if ok, e := Matches(context.Background(), s, dep, path, "", 1); e == nil || ok {
		t.Fatal("byte limit ignored")
	}
	other := dep
	other.ChainID = "10"
	if ok, e := Matches(context.Background(), s, other, path, "", DefaultMaxDecompressedBytes); e == nil || ok {
		t.Fatal("foreign deployment accepted")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	truncated := filepath.Join(t.TempDir(), "truncated.gz")
	if e = os.WriteFile(truncated, raw[:len(raw)-6], 0600); e != nil {
		t.Fatal(e)
	}
	if ok, e := Matches(context.Background(), s, dep, truncated, "", DefaultMaxDecompressedBytes); e == nil || ok {
		t.Fatal("gzip truncation accepted on empty database")
	}
	// A content match still requires a complete footer and no trailing records.
	complete := matchSQLite(t, dep, batches)
	h := header(dep)
	missing := filepath.Join(t.TempDir(), "missing-footer.gz")
	writeRecords(t, missing, []record{{Type: "header", Header: &h}, {Type: "block", Batch: &batches[0]}, {Type: "block", Batch: &batches[1]}})
	if ok, e := Matches(context.Background(), complete, dep, missing, "", DefaultMaxDecompressedBytes); e == nil || ok {
		t.Fatal("missing footer accepted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = Matches(cancelled, complete, dep, path, "", DefaultMaxDecompressedBytes); !errors.Is(e, context.Canceled) {
		t.Fatalf("cancellation %v", e)
	}
}
func TestMatchesPropagatesExportFailure(t *testing.T) {
	dep, batches := fixture()
	path := matchSource(t, dep, batches)
	sentinel := errors.New("export read failed")
	backend := &fakeStore{exportErr: sentinel}
	if _, e := Matches(context.Background(), backend, dep, path, "", DefaultMaxDecompressedBytes); !errors.Is(e, sentinel) {
		t.Fatal(e)
	}
}

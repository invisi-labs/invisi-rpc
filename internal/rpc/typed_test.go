package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/storage"
)

func TestPublicMethodAllowlistAndUnknownMethod(t *testing.T) {
	want := []string{"DetectCreditStates", "DetectNotes", "GetBlock", "GetBroadcasters", "GetCreditAllocation", "GetCreditRevocationBitmap", "GetCreditState", "GetCreditTransitions", "GetDeployment", "GetEvents", "GetMembership", "GetNote", "GetNoteUpdates", "GetReceivingAddress", "GetRoots", "GetStatus", "GetTransactionCreditUpdates", "GetTransactionNoteUpdates", "GetVersion", "IsCreditConsumed", "IsNoteSpent"}
	sort.Strings(want)
	typ := reflect.TypeOf((*API)(nil))
	got := make([]string, typ.NumMethod())
	for i := range got {
		got[i] = typ.Method(i).Name
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("public method set: got %v, want %v", got, want)
	}
	f, _ := anchor()
	r := call(t, testServer(t, f, true), "invisi_noSuchMethod", nil, "")
	if r.Error == nil || r.Error.Code != -32601 {
		t.Fatalf("unknown method response: %+v", r)
	}
}
func TestTypedRPCSQLiteStateGrantAndTransactionBoundaries(t *testing.T) {
	api, fake, _, tx := transactionFixture(t)
	api.Deployment.FromBlock = 10
	ctx := context.Background()
	s, e := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "rpc.db"), api.Deployment)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	api.Store = s
	owner, token := common.HexToAddress("1"), common.HexToAddress("2")
	note, state := common.HexToHash("3"), common.HexToHash("4")
	b := *fake.batch
	b.CreatedTrees = []uint64{1}
	b.Insertions = []model.Insertion{{TreeID: 1, Commitments: []common.Hash{note, state}}}
	b.Outputs = []model.NoteOutput{{TreeID: 1, Commitment: note, TransactionHash: tx}}
	b.CreditStates = []model.CreditState{{TreeID: 1, LeafIndex: 1, Commitment: state, TransactionHash: tx, Deliveries: []model.Delivery{{Clue: []byte{255}}, {Clue: []byte{255}}}}}
	b.Events = []model.Event{{Name: "CreditCreated", TransactionHash: tx, Data: []byte(`{"owner":"` + owner.Hex() + `","token":"` + token.Hex() + `","revocationShard":1,"revocationSlot":0}`)}}
	if e = s.Apply(ctx, b); e != nil {
		t.Fatal(e)
	}
	if v, e := api.GetNote(ctx, state, 10); e != nil || v != nil {
		t.Fatal("Credit returned through getNote")
	}
	if v, e := api.GetCreditState(ctx, note, 10); e != nil || v != nil {
		t.Fatal("Note returned through getCreditState")
	}
	if v, e := api.GetCreditState(ctx, state, 10); e != nil || v == nil || len(v.State.Deliveries) != 2 {
		t.Fatalf("state lookup %v %v", v, e)
	}
	h, e := HTTPHandler(api)
	if e != nil {
		t.Fatal(e)
	}
	rng := Range{10, 10, b.Block.Hash}
	r := call(t, h, "invisi_getNoteUpdates", []any{rng}, "")
	var notes NoteUpdates
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	if e = json.Unmarshal(r.Result, &notes); e != nil || len(notes.Notes) != 1 {
		t.Fatalf("notes %s %v", r.Result, e)
	}
	var fields map[string]json.RawMessage
	json.Unmarshal(r.Result, &fields)
	if fields["states"] != nil || fields["creditTransitions"] != nil {
		t.Fatal("mixed note updates")
	}
	tn, e := api.GetTransactionNoteUpdates(ctx, tx)
	if e != nil || len(tn.Notes) != 1 {
		t.Fatalf("transaction notes %+v %v", tn, e)
	}
	tc, e := api.GetTransactionCreditUpdates(ctx, tx)
	if e != nil || len(tc.States) != 1 || len(tc.Transitions) != 1 {
		t.Fatalf("transaction Credit %+v %v", tc, e)
	}
	a, e := api.GetCreditAllocation(ctx, owner, token, 10)
	if e != nil || a.Shard != "1" || a.Slot != 1 {
		t.Fatalf("allocation %+v %v", a, e)
	}
	bm, e := api.GetCreditRevocationBitmap(ctx, owner, token, "1", 0, 10)
	if e != nil || bm.Bitmap != "0" {
		t.Fatalf("bitmap %+v %v", bm, e)
	}
	if _, e = api.DetectCreditStates(ctx, rng, make([]byte, 32)); e == nil {
		t.Fatal("disabled detection accepted")
	}
}

func TestCreditDetectionKeepsBothEnvelopesAndNoNotes(t *testing.T) {
	ctx := context.Background()
	api, fake, _, tx := transactionFixture(t)
	api.Deployment.FromBlock = 10
	s, e := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "detection.db"), api.Deployment)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	api.Store = s
	api.detectionEnabled = true
	// A malformed clue must remain recoverable without guessing the recipient role.
	b := *fake.batch
	b.CreditStates = []model.CreditState{{TransactionHash: tx, Deliveries: []model.Delivery{{Clue: []byte{255}, Ciphertext: []byte{1}}, {Clue: []byte{255}, Ciphertext: []byte{2}}}}}
	if e = s.Apply(ctx, b); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile("../cryptography/testdata/fmd-rust-vectors.json")
	if e != nil {
		t.Fatal(e)
	}
	var vectors struct {
		Vectors []struct {
			DetectionKey string `json:"detectionKey"`
		}
	}
	if e = json.Unmarshal(raw, &vectors); e != nil {
		t.Fatal(e)
	}
	key, e := hex.DecodeString(vectors.Vectors[0].DetectionKey)
	if e != nil {
		t.Fatal(e)
	}
	rng := Range{10, 10, b.Block.Hash}
	v, e := api.DetectCreditStates(ctx, rng, key)
	if e != nil || len(v.Candidates) != 1 || !v.Candidates[0].Fallback || len(v.Candidates[0].State.Deliveries) != 2 {
		t.Fatalf("Credit detection %+v %v", v, e)
	}
	notes, e := api.DetectNotes(ctx, rng, key)
	if e != nil || len(notes.Candidates) != 0 {
		t.Fatalf("Credit leaked through Note detection %+v %v", notes, e)
	}
}

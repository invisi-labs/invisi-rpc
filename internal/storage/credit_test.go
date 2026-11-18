package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-labs/invisi-rpc/internal/model"
)

type creditReader interface {
	CreditState(context.Context, common.Hash, uint64) (*model.CreditStateLookup, error)
	ConsumedAt(context.Context, common.Hash, uint64, bool) (bool, error)
	CreditAllocationAt(context.Context, common.Address, common.Address, uint64) (string, uint16, error)
	CreditBitmapAt(context.Context, common.Address, common.Address, string, uint8, uint64) (string, error)
}

func TestCreditNoteIsolationHistoricalAndReorg(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			ctx := context.Background()
			dep := model.Deployment{ChainID: fmt.Sprint("credit-", time.Now().UnixNano()), FromBlock: 10}
			var s Backend
			if driver == "sqlite" {
				v, e := OpenSQLite(ctx, filepath.Join(t.TempDir(), "typed.db"), dep)
				if e != nil {
					t.Fatal(e)
				}
				s = v
			} else {
				url := os.Getenv("TEST_DATABASE_URL")
				if url == "" {
					t.Skip("TEST_DATABASE_URL not set")
				}
				v, e := Open(ctx, url, dep)
				if e != nil {
					t.Fatal(e)
				}
				s = v
				t.Cleanup(func() { v.pool.Exec(ctx, "DELETE FROM invisi_blocks WHERE deployment=$1", v.key) })
			}
			defer s.Close()
			c := s.(creditReader)
			owner, token := common.HexToAddress("1"), common.HexToAddress("2")
			state, note := common.HexToHash("3"), common.HexToHash("4")
			creditNull, noteNull := common.HexToHash("5"), common.HexToHash("6")
			b := model.Batch{Block: model.Block{Number: 10, Hash: common.HexToHash("10")}, CreatedTrees: []uint64{1}, Insertions: []model.Insertion{{TreeID: 1, Commitments: []common.Hash{state, note}}}, Outputs: []model.NoteOutput{{TreeID: 1, LeafIndex: 1, Commitment: note}}, CreditStates: []model.CreditState{{TreeID: 1, Commitment: state, Deliveries: []model.Delivery{{Ciphertext: []byte{1}}, {Ciphertext: []byte{2}}}}}, Events: []model.Event{{Name: "CreditCreated", Data: []byte(fmt.Sprintf(`{"owner":%q,"token":%q,"revocationShard":1,"revocationSlot":0}`, owner.Hex(), token.Hex()))}}}
			if e := s.Apply(ctx, b); e != nil {
				t.Fatal(e)
			}
			if v, e := s.Note(ctx, state, 10); e != nil || v != nil {
				t.Fatalf("Credit leaked as Note: %v %v", v, e)
			}
			if v, e := c.CreditState(ctx, note, 10); e != nil || v != nil {
				t.Fatalf("Note leaked as Credit: %v %v", v, e)
			}
			if v, e := c.CreditState(ctx, state, 10); e != nil || v == nil || len(v.State.Deliveries) != 2 || len(v.Membership.Siblings) != 16 {
				t.Fatalf("state lookup: %+v %v", v, e)
			}
			b2 := model.Batch{Block: model.Block{Number: 11, Hash: common.HexToHash("11"), ParentHash: b.Block.Hash}, Nullifiers: []common.Hash{noteNull}, CreditNullifiers: []common.Hash{creditNull}, Events: []model.Event{{Name: "CreditRevoked", Data: []byte(fmt.Sprintf(`{"owner":%q,"token":%q,"shard":1,"slot":0}`, owner.Hex(), token.Hex()))}}}
			if e := s.Apply(ctx, b2); e != nil {
				t.Fatal(e)
			}
			for _, credit := range []bool{false, true} {
				h := noteNull
				if credit {
					h = creditNull
				}
				if v, e := c.ConsumedAt(ctx, h, 10, credit); e != nil || v {
					t.Fatal("future consumption")
				}
				if v, e := c.ConsumedAt(ctx, h, 11, credit); e != nil || !v {
					t.Fatal("missing consumption")
				}
				if v, e := c.ConsumedAt(ctx, h, 11, !credit); e != nil || v {
					t.Fatal("mixed nullifier domains")
				}
			}
			if sh, slot, e := c.CreditAllocationAt(ctx, owner, token, 11); e != nil || sh != "1" || slot != 1 {
				t.Fatalf("allocation %s %d %v", sh, slot, e)
			}
			if bits, e := c.CreditBitmapAt(ctx, owner, token, "1", 0, 10); e != nil || bits != "0" {
				t.Fatal("future revoke")
			}
			if bits, e := c.CreditBitmapAt(ctx, owner, token, "1", 0, 11); e != nil || bits != "1" {
				t.Fatalf("bitmap %s %v", bits, e)
			}
			if e := s.Rollback(ctx, 10); e != nil {
				t.Fatal(e)
			}
			if bits, e := c.CreditBitmapAt(ctx, owner, token, "1", 0, 11); e != nil || bits != "0" {
				t.Fatal("revocation survived reorg")
			}
			if v, e := c.ConsumedAt(ctx, creditNull, 11, true); e != nil || v {
				t.Fatal("consumption survived reorg")
			}
		})
	}
}

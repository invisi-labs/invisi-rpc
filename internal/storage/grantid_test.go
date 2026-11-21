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

func TestGrantIDHistoryAndTerminalPreRevoke(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) { testGrantIDHistory(t, driver) })
	}
}
func testGrantIDHistory(t *testing.T, driver string) {
	ctx := context.Background()
	dep := model.Deployment{ChainID: fmt.Sprint("grant-", time.Now().UnixNano()), FromBlock: 10}
	path := filepath.Join(t.TempDir(), "grants.db")
	var s interface {
		Backend
		CreditStatusAt(context.Context, common.Address, common.Address, string, uint64) (uint8, error)
	}
	if driver == "sqlite" {
		v, e := OpenSQLite(ctx, path, dep)
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
	}
	defer s.Close()
	var e error
	owner, token := common.HexToAddress("1"), common.HexToAddress("2")
	id := "340282366920938463463374607431768211455"
	event := func(kind, id string) model.Event {
		return model.Event{Name: kind, Data: []byte(fmt.Sprintf(`{"owner":%q,"token":%q,"grantId":%q}`, owner.Hex(), token.Hex(), id))}
	}
	b := model.Batch{Block: model.Block{Number: 10, Hash: common.HexToHash("10")}, Events: []model.Event{event("CreditRevoked", id)}}
	if e = s.Apply(ctx, b); e != nil {
		t.Fatal(e)
	}
	for _, v := range []struct {
		owner, token common.Address
		id           string
		at           uint64
		want         uint8
	}{{owner, token, id, 9, 0}, {owner, token, id, 10, 2}, {token, token, id, 10, 0}, {owner, owner, id, 10, 0}, {owner, token, "1", 10, 0}} {
		got, e := s.CreditStatusAt(ctx, v.owner, v.token, v.id, v.at)
		if e != nil || got != v.want {
			t.Fatalf("status %v %v want %v", got, e, v.want)
		}
	}
	for _, kind := range []string{"CreditCreated", "CreditRevoked"} {
		next := model.Batch{Block: model.Block{Number: 11, Hash: common.HexToHash("11"), ParentHash: b.Block.Hash}, Events: []model.Event{event(kind, id)}}
		if e = s.Apply(ctx, next); e == nil {
			t.Fatalf("accepted %s after terminal revoke", kind)
		}
	}
	if e = s.Rollback(ctx, 9); e != nil {
		t.Fatal(e)
	}
	b.Events = []model.Event{event("CreditCreated", id)}
	if e = s.Apply(ctx, b); e != nil {
		t.Fatal(e)
	}
	if got, e := s.CreditStatusAt(ctx, owner, token, id, 10); e != nil || got != 1 {
		t.Fatalf("reorg rebuild %v %v", got, e)
	}
	// A shared database must never let another chain/Core inherit this ID.
	for _, other := range []model.Deployment{{ChainID: dep.ChainID + "-other", FromBlock: 10}, {ChainID: dep.ChainID, Protocol: common.HexToAddress("99"), FromBlock: 10}} {
		var status uint8
		if driver == "sqlite" {
			v, err := OpenSQLite(ctx, path, other)
			if err != nil {
				t.Fatal(err)
			}
			status, e = v.CreditStatusAt(ctx, owner, token, id, 10)
			v.Close()
		} else {
			v, err := Open(ctx, os.Getenv("TEST_DATABASE_URL"), other)
			if err != nil {
				t.Fatal(err)
			}
			status, e = v.CreditStatusAt(ctx, owner, token, id, 10)
			v.Close()
		}
		if e != nil || status != 0 {
			t.Fatalf("grant crossed deployment %d %v", status, e)
		}
	}
	if e = s.Apply(ctx, model.Batch{Block: model.Block{Number: 11, Hash: common.HexToHash("11"), ParentHash: b.Block.Hash}, Events: []model.Event{event("CreditCreated", id)}}); e == nil {
		t.Fatal("reused active grant ID")
	}

}

func TestRejectLegacyAndNoncanonicalGrantEvents(t *testing.T) {
	for _, raw := range []string{`{"revocationShard":"1","revocationSlot":0}`, `{"grantId":"0"}`, `{"grantId":"01"}`, `{"grantId":"340282366920938463463374607431768211456"}`, `{"grantId":1}`} {
		if _, e := grants(model.Batch{Events: []model.Event{{Name: "CreditCreated", Data: []byte(raw)}}}); e == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestGrantIDSameBlockCanonicalLogOrderAndAtomicity(t *testing.T) {
	for _, revokeFirst := range []bool{false, true} {
		name := "create_then_revoke"
		if revokeFirst {
			name = "pre_revoke_then_create_rejected"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s, e := OpenSQLite(ctx, filepath.Join(t.TempDir(), "order.db"), model.Deployment{ChainID: "1", FromBlock: 10})
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			owner, token := common.HexToAddress("1"), common.HexToAddress("2")
			data := []byte(fmt.Sprintf(`{"owner":%q,"token":%q,"grantId":"1"}`, owner.Hex(), token.Hex()))
			first, second := "CreditCreated", "CreditRevoked"
			if revokeFirst {
				first, second = second, first
			}
			// Input slices need not be sorted; canonical EVM log positions determine order.
			b := model.Batch{Block: model.Block{Number: 10, Hash: common.HexToHash("10")}, Events: []model.Event{{Name: second, LogIndex: 2, Data: data}, {Name: first, LogIndex: 1, Data: data}}}
			e = s.Apply(ctx, b)
			if revokeFirst {
				if e == nil {
					t.Fatal("accepted creation after same-block pre-revoke")
				}
				if head, e := s.Checkpoint(ctx); e != nil || head != nil {
					t.Fatalf("rejected batch left checkpoint %v %v", head, e)
				}
				if status, e := s.CreditStatusAt(ctx, owner, token, "1", 10); e != nil || status != 0 {
					t.Fatalf("rejected batch left partial status %d %v", status, e)
				}
				if events, e := s.Events(ctx, 10, 10, 10); e != nil || len(events) != 0 {
					t.Fatalf("rejected batch left events %v %v", events, e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if status, e := s.CreditStatusAt(ctx, owner, token, "1", 10); e != nil || status != 2 {
				t.Fatalf("same-block 0->1->2 status %d %v", status, e)
			}
		})
	}
}

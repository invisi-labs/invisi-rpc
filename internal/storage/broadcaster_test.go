package storage

import (
	"context"
	"fmt"
	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSQLiteBroadcasterLifecycle(t *testing.T) {
	ctx := context.Background()
	dep := model.Deployment{ChainID: "97", Protocol: common.HexToAddress("0x1"), FromBlock: 10, BroadcasterRegistry: &model.ContractDeployment{Address: common.HexToAddress("0x2"), RuntimeCodeHash: common.HexToHash("0x3"), FromBlock: 8}}
	path := filepath.Join(t.TempDir(), "broadcaster.db")
	s, err := OpenSQLite(ctx, path, dep)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	operator := common.HexToAddress("0x1234")
	on, off := true, false
	var parent common.Hash
	for n := uint64(8); n <= 10; n++ {
		b := model.Batch{Block: model.Block{Number: n, Hash: common.BigToHash(new(big.Int).SetUint64(n)), ParentHash: parent}}
		parent = b.Block.Hash
		if n == 8 {
			b.Broadcasters = []model.BroadcasterUpdate{{Operator: operator, Endpoint: "https://first.example", QuoteSigner: operator, FeeRecipient: operator, LogIndex: 1}, {Operator: operator, Enabled: &on, LogIndex: 2}}
		}
		if n == 9 {
			b.Broadcasters = []model.BroadcasterUpdate{{Operator: operator, Enabled: &off, LogIndex: 1}}
		}
		if n == 10 {
			b.Broadcasters = []model.BroadcasterUpdate{{Operator: operator, Endpoint: "https://updated.example", QuoteSigner: operator, FeeRecipient: operator, LogIndex: 1}, {Operator: operator, Enabled: &off, LogIndex: 2}}
		}
		if err = s.Apply(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.Broadcasters(ctx, model.BroadcasterQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Broadcasters) != 1 || page.Broadcasters[0].Enabled || page.Broadcasters[0].Endpoint != "https://updated.example" {
		t.Fatalf("unexpected page: %+v", page)
	}
	page, err = s.Broadcasters(ctx, model.BroadcasterQuery{Limit: 1, Enabled: &on})
	if err != nil || len(page.Broadcasters) != 0 {
		t.Fatalf("enabled filter: %+v %v", page, err)
	}
	if err = s.Rollback(ctx, 8); err != nil {
		t.Fatal(err)
	}
	page, err = s.Broadcasters(ctx, model.BroadcasterQuery{Limit: 1})
	if err != nil || !page.Broadcasters[0].Enabled || page.Broadcasters[0].Endpoint != "https://first.example" {
		t.Fatalf("rollback: %+v %v", page, err)
	}
	s.Close()
	s, err = OpenSQLite(ctx, path, dep)
	if err != nil {
		t.Fatal(err)
	}
	page, err = s.Broadcasters(ctx, model.BroadcasterQuery{Limit: 1})
	if err != nil || len(page.Broadcasters) != 1 {
		t.Fatal("restart lost index", err)
	}
}

func TestSQLiteBroadcasterPaginationAndPins(t *testing.T) {
	ctx := context.Background()
	dep := model.Deployment{ChainID: "97", Protocol: common.HexToAddress("0x1"), FromBlock: 8}
	path := filepath.Join(t.TempDir(), "pins.db")
	s, err := OpenSQLite(ctx, path, dep)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Apply(ctx, model.Batch{Block: model.Block{Number: 8, Hash: common.HexToHash("0x8")}}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	dep.BroadcasterRegistry = &model.ContractDeployment{Address: common.HexToAddress("0x2"), RuntimeCodeHash: common.HexToHash("0x3"), FromBlock: 8}
	if s, err = OpenSQLite(ctx, path, dep); err == nil {
		s.Close()
		t.Fatal("added registry to already indexed history")
	}
	path = filepath.Join(t.TempDir(), "pages.db")
	s, err = OpenSQLite(ctx, path, dep)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	on := true
	updates := []model.BroadcasterUpdate{}
	for i := int64(1); i <= 3; i++ {
		operator := common.BigToAddress(big.NewInt(i))
		updates = append(updates, model.BroadcasterUpdate{Operator: operator, Endpoint: "https://relay.example", QuoteSigner: operator, FeeRecipient: operator, LogIndex: uint(i * 2)}, model.BroadcasterUpdate{Operator: operator, Enabled: &on, LogIndex: uint(i*2 + 1)})
	}
	b := model.Batch{Block: model.Block{Number: 8, Hash: common.HexToHash("0x8")}, Broadcasters: updates}
	if err = s.Apply(ctx, b); err != nil {
		t.Fatal(err)
	}
	first, err := s.Broadcasters(ctx, model.BroadcasterQuery{Limit: 2})
	if err != nil || len(first.Broadcasters) != 2 || first.Next == nil {
		t.Fatalf("first: %+v %v", first, err)
	}
	second, err := s.Broadcasters(ctx, model.BroadcasterQuery{Limit: 2, After: *first.Next, Block: &first.Block})
	if err != nil || len(second.Broadcasters) != 1 || second.Next != nil {
		t.Fatalf("second: %+v %v", second, err)
	}
	bad := first.Block
	bad.Hash = common.HexToHash("0xdead")
	if _, err = s.Broadcasters(ctx, model.BroadcasterQuery{Limit: 2, Block: &bad}); err == nil {
		t.Fatal("accepted reorganized anchor")
	}
	if _, err = s.Broadcasters(ctx, model.BroadcasterQuery{Limit: 2, After: *first.Next}); err == nil {
		t.Fatal("unanchored pagination")
	}
}

func TestPostgresBroadcasterRollbackAndImport(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	dep := model.Deployment{ChainID: fmt.Sprintf("broadcaster-%d", time.Now().UnixNano()), Protocol: common.HexToAddress("0x1"), FromBlock: 8, BroadcasterRegistry: &model.ContractDeployment{Address: common.HexToAddress("0x2"), RuntimeCodeHash: common.HexToHash("0x3"), FromBlock: 8}}
	s, err := Open(ctx, url, dep)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	defer s.pool.Exec(ctx, "DELETE FROM invisi_blocks WHERE deployment=$1", s.key)
	operator := common.HexToAddress("0x1234")
	on, off := true, false
	first := model.Batch{Block: model.Block{Number: 8, Hash: common.HexToHash("0x8")}, Broadcasters: []model.BroadcasterUpdate{{Operator: operator, Endpoint: "https://first.example", QuoteSigner: operator, FeeRecipient: operator, LogIndex: 1}, {Operator: operator, Enabled: &on, LogIndex: 2}}}
	if err = s.Apply(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err = s.Apply(ctx, first); err != nil {
		t.Fatal("idempotent replay", err)
	}
	second := model.Batch{Block: model.Block{Number: 9, Hash: common.HexToHash("0x9"), ParentHash: first.Block.Hash}, Broadcasters: []model.BroadcasterUpdate{{Operator: operator, Enabled: &off, LogIndex: 1}}}
	if err = s.Apply(ctx, second); err != nil {
		t.Fatal(err)
	}
	page, err := s.Broadcasters(ctx, model.BroadcasterQuery{Limit: 10, Enabled: &off})
	if err != nil || len(page.Broadcasters) != 1 || page.Broadcasters[0].Enabled {
		t.Fatalf("availability: %+v %v", page, err)
	}
	var exported []model.Batch
	if _, err = s.Export(ctx, func(b model.Batch) error { exported = append(exported, b); return nil }); err != nil {
		t.Fatal(err)
	}
	destDep := dep
	destDep.ChainID += "-import"
	dest, err := Open(ctx, url, destDep)
	if err != nil {
		t.Fatal(err)
	}
	defer dest.Close()
	defer dest.pool.Exec(ctx, "DELETE FROM invisi_blocks WHERE deployment=$1", dest.key)
	next := 0
	if err = dest.Import(ctx, func() (model.Batch, error) {
		if next == len(exported) {
			return model.Batch{}, io.EOF
		}
		b := exported[next]
		next++
		return b, nil
	}); err != nil {
		t.Fatal(err)
	}
	page, err = dest.Broadcasters(ctx, model.BroadcasterQuery{Limit: 10})
	if err != nil || len(page.Broadcasters) != 1 || page.Broadcasters[0].Enabled {
		t.Fatalf("import: %+v %v", page, err)
	}
	if err = s.Rollback(ctx, 8); err != nil {
		t.Fatal(err)
	}
	page, err = s.Broadcasters(ctx, model.BroadcasterQuery{Limit: 10})
	if err != nil || !page.Broadcasters[0].Enabled {
		t.Fatalf("rollback: %+v %v", page, err)
	}
	changed := dep
	changed.BroadcasterRegistry = &model.ContractDeployment{Address: common.HexToAddress("0x9"), RuntimeCodeHash: common.HexToHash("0x3"), FromBlock: 8}
	if reopened, err := Open(ctx, url, changed); err == nil {
		reopened.Close()
		t.Fatal("changed registry accepted")
	}
}

type broadcasterFactory func(*testing.T, model.Deployment) Backend

func broadcasterDeployment() model.Deployment {
	return model.Deployment{ChainID: "97", Protocol: common.HexToAddress("0x123"), FromBlock: 10, BroadcasterRegistry: &model.ContractDeployment{Address: common.HexToAddress("0x456"), RuntimeCodeHash: common.HexToHash("0x789"), FromBlock: 8}}
}
func broadcasterSet(operator common.Address, endpoint string, index uint) model.BroadcasterUpdate {
	return model.BroadcasterUpdate{Operator: operator, Endpoint: endpoint, MetadataURI: "https://metadata.example", QuoteSigner: operator, FeeRecipient: operator, LogIndex: index}
}
func TestBroadcasterFullStateHistory(t *testing.T) {
	factories := map[string]broadcasterFactory{"sqlite": func(t *testing.T, d model.Deployment) Backend {
		s, e := OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "index.db"), d)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(s.Close)
		return s
	}, "postgres": func(t *testing.T, d model.Deployment) Backend {
		url := os.Getenv("TEST_DATABASE_URL")
		if url == "" {
			t.Skip("TEST_DATABASE_URL is not set")
		}
		d.ChainID = fmt.Sprint("broadcaster-", time.Now().UnixNano())
		s, e := Open(context.Background(), url, d)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() {
			s.pool.Exec(context.Background(), "DELETE FROM invisi_blocks WHERE deployment=$1", s.key)
			s.Close()
		})
		return s
	}}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			dep := broadcasterDeployment()
			s := factory(t, dep)
			a, b, c := common.HexToAddress("0x10"), common.HexToAddress("0x01"), common.HexToAddress("0x20")
			off, on := false, true
			b8 := model.Batch{Block: model.Block{Number: 8, Hash: common.HexToHash("0x08")}, Broadcasters: []model.BroadcasterUpdate{broadcasterSet(a, "https://a.example", 1), broadcasterSet(b, "https://b.example", 2)}}
			if e := s.Apply(ctx, b8); e != nil {
				t.Fatal(e)
			}
			if e := s.Apply(ctx, b8); e != nil {
				t.Fatal(e)
			}
			page, e := s.Broadcasters(ctx, model.BroadcasterQuery{Limit: 1})
			if e != nil || len(page.Broadcasters) != 1 || page.Broadcasters[0].Operator != b || !page.Broadcasters[0].Enabled || page.Next == nil || *page.Next != b {
				t.Fatalf("set-only and ordering: %+v %v", page, e)
			}
			b9 := model.Batch{Block: model.Block{Number: 9, Hash: common.HexToHash("0x09"), ParentHash: b8.Block.Hash}, Broadcasters: []model.BroadcasterUpdate{{Operator: a, Enabled: &off, LogIndex: 1}, broadcasterSet(a, "https://updated.example", 2), {Operator: b, Enabled: &off, LogIndex: 3}}}
			if e = s.Apply(ctx, b9); e != nil {
				t.Fatal(e)
			}
			page, e = s.Broadcasters(ctx, model.BroadcasterQuery{Enabled: &on})
			if e != nil || len(page.Broadcasters) != 0 {
				t.Fatalf("latest enabled filter must not revive history: %+v %v", page, e)
			}
			page, e = s.Broadcasters(ctx, model.BroadcasterQuery{Enabled: &off})
			if e != nil || len(page.Broadcasters) != 2 || page.Broadcasters[1].Endpoint != "https://updated.example" || page.Broadcasters[1].Block.Hash != b9.Block.Hash {
				t.Fatalf("set preserves disable: %+v %v", page, e)
			}
			page, e = s.Broadcasters(ctx, model.BroadcasterQuery{After: b, Block: &b8.Block, Limit: 1})
			if e != nil || len(page.Broadcasters) != 1 || page.Broadcasters[0].Operator != a || !page.Broadcasters[0].Enabled {
				t.Fatalf("historical pagination: %+v %v", page, e)
			}
			if _, e = s.Broadcasters(ctx, model.BroadcasterQuery{After: b}); e == nil {
				t.Fatal("unanchored cursor accepted")
			}
			if _, e = s.Broadcasters(ctx, model.BroadcasterQuery{Limit: 101}); e == nil {
				t.Fatal("excess limit accepted")
			}
			b10 := model.Batch{Block: model.Block{Number: 10, Hash: common.HexToHash("0x10"), ParentHash: b9.Block.Hash}, Broadcasters: []model.BroadcasterUpdate{{Operator: a, Enabled: &on, LogIndex: 1}, broadcasterSet(c, "https://c.example", 2)}}
			if e = s.Apply(ctx, b10); e != nil {
				t.Fatal(e)
			}
			batches := []model.Batch{}
			head, e := s.Export(ctx, func(batch model.Batch) error { batches = append(batches, batch); return nil })
			if e != nil || head.Hash != b10.Block.Hash {
				t.Fatal("export anchor", e)
			}
			restored := factory(t, dep)
			index := 0
			if e = restored.Import(ctx, func() (model.Batch, error) {
				if index == len(batches) {
					return model.Batch{}, io.EOF
				}
				batch := batches[index]
				index++
				return batch, nil
			}); e != nil {
				t.Fatal(e)
			}
			restoredPage, e := restored.Broadcasters(ctx, model.BroadcasterQuery{Enabled: &on})
			if e != nil || len(restoredPage.Broadcasters) != 2 || restoredPage.Broadcasters[0].Operator != a || restoredPage.Block != b10.Block {
				t.Fatalf("snapshot replay %+v %v", restoredPage, e)
			}
			if e = s.Rollback(ctx, 8); e != nil {
				t.Fatal(e)
			}
			page, e = s.Broadcasters(ctx, model.BroadcasterQuery{})
			if e != nil || len(page.Broadcasters) != 2 || !page.Broadcasters[1].Enabled || page.Broadcasters[1].Endpoint != "https://a.example" {
				t.Fatalf("rollback state %+v %v", page, e)
			}
			if _, e = s.Broadcasters(ctx, model.BroadcasterQuery{Block: &b10.Block}); e == nil {
				t.Fatal("reorganized anchor accepted")
			}
			bad := b9
			bad.Broadcasters = []model.BroadcasterUpdate{{Operator: c, Enabled: &on, LogIndex: 1}}
			if e = s.Apply(ctx, bad); e == nil {
				t.Fatal("availability without registration accepted")
			}
			checkpoint, _ := s.Checkpoint(ctx)
			if checkpoint.Hash != b8.Block.Hash {
				t.Fatal("failed apply changed checkpoint")
			}
			bad.Broadcasters = []model.BroadcasterUpdate{broadcasterSet(a, "https://"+strings.Repeat("a", 2048), 1)}
			if e = s.Apply(ctx, bad); e == nil {
				t.Fatal("oversized endpoint accepted")
			}
			bad.Broadcasters = []model.BroadcasterUpdate{broadcasterSet(a, "https://a.example", 2), broadcasterSet(b, "https://b.example", 1)}
			if e = s.Apply(ctx, bad); e == nil {
				t.Fatal("out of order events accepted")
			}
		})
	}
}
func TestSQLiteBroadcasterProfileCoverageGuards(t *testing.T) {
	ctx := context.Background()
	dep := broadcasterDeployment()
	path := filepath.Join(t.TempDir(), "index.db")
	s, e := OpenSQLite(ctx, path, dep)
	if e != nil {
		t.Fatal(e)
	}
	batch := model.Batch{Block: model.Block{Number: 8, Hash: common.HexToHash("0x08")}}
	if e = s.Apply(ctx, batch); e != nil {
		t.Fatal(e)
	}
	s.Close()
	changed := dep
	profile := *dep.BroadcasterRegistry
	profile.Address = common.HexToAddress("0x999")
	changed.BroadcasterRegistry = &profile
	if store, e := OpenSQLite(ctx, path, changed); e == nil {
		store.Close()
		t.Fatal("changed profile accepted")
	}
	s, e = OpenSQLite(ctx, path, dep)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.pool.ExecContext(ctx, "DELETE FROM invisi_broadcaster_formats WHERE deployment=?", s.key); e != nil {
		t.Fatal(e)
	}
	s.Close()
	if store, e := OpenSQLite(ctx, path, dep); e == nil {
		store.Close()
		t.Fatal("legacy uncovered index accepted")
	}
	path = filepath.Join(t.TempDir(), "old.db")
	absent := dep
	absent.BroadcasterRegistry = nil
	s, e = OpenSQLite(ctx, path, absent)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Apply(ctx, model.Batch{Block: model.Block{Number: 10, Hash: common.HexToHash("0x10")}}); e != nil {
		t.Fatal(e)
	}
	s.pool.ExecContext(ctx, "DELETE FROM invisi_broadcaster_profiles WHERE deployment=?", s.key)
	s.pool.ExecContext(ctx, "DELETE FROM invisi_broadcaster_formats WHERE deployment=?", s.key)
	s.Close()
	s, e = OpenSQLite(ctx, path, absent)
	if e != nil {
		t.Fatalf("absent optional metadata broke existing index: %v", e)
	}
	s.Close()
	if store, e := OpenSQLite(ctx, path, dep); e == nil {
		store.Close()
		t.Fatal("added profile claimed existing coverage")
	}
}

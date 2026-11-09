package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/snapshot"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/storage"
	"github.com/jackc/pgx/v5"
)

func TestSnapshotSQLitePostgresSQLite(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, databaseURL)
	must(t, err)
	defer admin.Close(context.Background())
	schema := fmt.Sprintf("invisi_portability_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted)
	must(t, err)
	defer admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
	parsed, err := url.Parse(databaseURL)
	must(t, err)
	q := parsed.Query()
	q.Set("search_path", schema)
	parsed.RawQuery = q.Encode()
	dep := model.Deployment{ChainID: "31337", Protocol: common.HexToAddress("0x123"), FromBlock: 100, RuntimeCodeHash: common.HexToHash("0x456")}
	source, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "source.sqlite"), dep)
	must(t, err)
	defer source.Close()
	postgres, err := storage.OpenPostgres(ctx, parsed.String(), dep)
	must(t, err)
	defer postgres.Close()
	destination, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "destination.sqlite"), dep)
	must(t, err)
	defer destination.Close()
	first := model.Batch{Block: model.Block{Number: 100, Hash: common.HexToHash("0x100")}, CreatedTrees: []uint64{7}, Insertions: []model.Insertion{{TreeID: 7, Commitments: []common.Hash{common.HexToHash("0x1"), common.HexToHash("0x2")}}}, Outputs: []model.NoteOutput{{TreeID: 7, Commitment: common.HexToHash("0x1"), Ciphertext: []byte{1, 2, 3}, Clue: []byte{4, 5, 6}}}, Events: []model.Event{{Name: "Shield", Data: json.RawMessage(`{"amount":3}`)}}, Logs: []types.Log{{Address: dep.Protocol, BlockNumber: 100, BlockHash: common.HexToHash("0x100"), Topics: []common.Hash{common.HexToHash("0x789")}, Data: []byte{1, 2}}}}
	second := model.Batch{Block: model.Block{Number: 101, Hash: common.HexToHash("0x101"), ParentHash: first.Block.Hash}, Insertions: []model.Insertion{{TreeID: 7, Start: 2, Commitments: []common.Hash{common.HexToHash("0x3")}}}, Nullifiers: []common.Hash{common.HexToHash("0xabc")}}
	owner, token := common.HexToAddress("1"), common.HexToAddress("2")
	first.CreditStates = []model.CreditState{{TreeID: 7, LeafIndex: 1, Commitment: common.HexToHash("2"), Deliveries: []model.Delivery{{Ciphertext: []byte{1}}, {Ciphertext: []byte{2}}}}}
	first.Events = append(first.Events, model.Event{Name: "CreditCreated", LogIndex: 1, Data: json.RawMessage(fmt.Sprintf(`{"owner":%q,"token":%q,"revocationShard":"1","revocationSlot":0}`, owner.Hex(), token.Hex()))})
	second.CreditNullifiers = []common.Hash{common.HexToHash("def")}
	second.Events = []model.Event{{Name: "CreditRevoked", LogIndex: 2, Data: json.RawMessage(fmt.Sprintf(`{"owner":%q,"token":%q,"shard":"1","slot":0}`, owner.Hex(), token.Hex()))}}
	must(t, source.Apply(ctx, first))
	must(t, source.Apply(ctx, second))
	originalRoots, err := source.Roots(ctx, 7, 10)
	must(t, err)
	exportRestore := func(from, to storage.Backend) {
		t.Helper()
		path, e := snapshot.Export(ctx, from, dep, t.TempDir())
		must(t, e)
		sidecar, e := os.ReadFile(path + ".sha256")
		must(t, e)
		must(t, snapshot.Restore(ctx, to, dep, path, strings.Fields(string(sidecar))[0]))
		matched, e := snapshot.Matches(ctx, to, dep, path, strings.Fields(string(sidecar))[0], snapshot.DefaultMaxDecompressedBytes)
		must(t, e)
		if !matched {
			t.Fatal("restored backend does not match its source snapshot")
		}
	}
	exportRestore(source, postgres)
	exportRestore(postgres, destination)
	for name, backend := range map[string]storage.Backend{"postgres": postgres, "sqlite": destination} {
		t.Run(name, func(t *testing.T) {
			head, e := backend.Checkpoint(ctx)
			must(t, e)
			if head == nil || *head != second.Block {
				t.Fatalf("checkpoint mismatch: %+v", head)
			}
			for _, want := range []model.Batch{first, second} {
				got, e := backend.Batch(ctx, want.Block.Number)
				must(t, e)
				a, e := json.Marshal(want)
				must(t, e)
				b, e := json.Marshal(got)
				must(t, e)
				var av, bv any
				must(t, json.Unmarshal(a, &av))
				must(t, json.Unmarshal(b, &bv))
				if !reflect.DeepEqual(av, bv) {
					t.Fatalf("batch %d changed across backends\n%s\n%s", want.Block.Number, a, b)
				}
			}
			roots, e := backend.Roots(ctx, 7, 10)
			must(t, e)
			if !reflect.DeepEqual(roots, originalRoots) {
				t.Fatalf("root history differs: %+v / %+v", roots, originalRoots)
			}
			for _, root := range roots[:2] {
				membership, e := backend.Membership(ctx, 7, 0, root.Root)
				must(t, e)
				verifyMembership(t, membership)
			}
			credit := backend.(interface {
				CreditState(context.Context, common.Hash, uint64) (*model.CreditStateLookup, error)
				ConsumedAt(context.Context, common.Hash, uint64, bool) (bool, error)
				CreditBitmapAt(context.Context, common.Address, common.Address, string, uint8, uint64) (string, error)
			})
			state, e := credit.CreditState(ctx, common.HexToHash("2"), 101)
			must(t, e)
			if state == nil || len(state.State.Deliveries) != 2 {
				t.Fatal("Credit envelopes lost during replay")
			}
			used, e := credit.ConsumedAt(ctx, second.CreditNullifiers[0], 101, true)
			must(t, e)
			if !used {
				t.Fatal("Credit consumption lost")
			}
			bitmap, e := credit.CreditBitmapAt(ctx, owner, token, "1", 0, 101)
			must(t, e)
			if bitmap != "1" {
				t.Fatal("revocation lost")
			}
			spent, e := backend.IsSpent(ctx, second.Nullifiers[0])
			must(t, e)
			if !spent {
				t.Fatal("nullifier missing after restore")
			}
			events, e := backend.Events(ctx, 100, 101, 10)
			must(t, e)
			outputs, e := backend.Outputs(ctx, 100, 101, 10)
			must(t, e)
			if len(events) != 3 || len(outputs) != 1 {
				t.Fatalf("events/outputs lost: %d/%d", len(events), len(outputs))
			}
		})
	}
	must(t, destination.Rollback(ctx, 100))
	spent, err := destination.IsSpent(ctx, second.Nullifiers[0])
	must(t, err)
	if spent {
		t.Fatal("restored nullifier survived rollback")
	}
	historical, err := destination.Membership(ctx, 7, 0, originalRoots[1].Root)
	must(t, err)
	verifyMembership(t, historical)
	t.Log("SQLite → gzip snapshot → PostgreSQL → gzip snapshot → SQLite preserved batches, logs, events, outputs, nullifiers, historical Merkle paths, and rollback")
}

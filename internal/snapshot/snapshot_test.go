package snapshot

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-rpc/internal/model"
	"github.com/invisi-lab/invisi-rpc/internal/storage"
	sdkcontracts "github.com/invisi-lab/invisi-sdk-go/protocol/contracts"
)

type fakeStore struct {
	storage.Backend
	batches   []model.Batch
	head      *model.Block
	imports   int
	committed []model.Batch
	exportErr error
	onImport  func()
}

func (s *fakeStore) Export(ctx context.Context, consume func(model.Batch) error) (*model.Block, error) {
	for _, b := range s.batches {
		if err := consume(b); err != nil {
			return nil, err
		}
	}
	return s.head, s.exportErr
}
func (s *fakeStore) Import(ctx context.Context, next func() (model.Batch, error)) error {
	s.imports++
	if s.onImport != nil {
		s.onImport()
	}
	var staged []model.Batch
	for {
		b, err := next()
		if errors.Is(err, io.EOF) {
			s.committed = staged
			return nil
		}
		if err != nil {
			return err
		}
		staged = append(staged, b)
	}
}
func fixture() (model.Deployment, []model.Batch) {
	dep := model.Deployment{ChainID: "31337", Protocol: common.HexToAddress("1234"), FromBlock: 10, RuntimeCodeHash: common.HexToHash("abcd")}
	bs := []model.Batch{{Block: model.Block{Number: 10, Hash: common.HexToHash("10")}, CreatedTrees: []uint64{1}}, {Block: model.Block{Number: 11, Hash: common.HexToHash("11"), ParentHash: common.HexToHash("10")}, Insertions: []model.Insertion{{TreeID: 1, Commitments: []common.Hash{common.HexToHash("01")}}}, Nullifiers: []common.Hash{common.HexToHash("02")}}}
	return dep, bs
}
func fileHash(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func writeRecords(t *testing.T, path string, records []record) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	enc := json.NewEncoder(gz)
	for _, rec := range records {
		if err = enc.Encode(rec); err != nil {
			t.Fatal(err)
		}
	}
	if err = gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestExportRestoreAndManifest(t *testing.T) {
	ctx := context.Background()
	dep, bs := fixture()
	source := &fakeStore{batches: bs, head: &bs[1].Block}
	path, err := Export(ctx, source, dep, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, filepath.Join(dep.ChainID, strings.ToLower(dep.Protocol.Hex()))) {
		t.Fatal("missing deployment directory")
	}
	for _, p := range []string{path, path + ".sha256", filepath.Join(filepath.Dir(path), "latest.json")} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("insecure permissions %s: %o", p, info.Mode().Perm())
		}
	}
	inspected, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Deployment != dep || inspected.Checkpoint == nil || *inspected.Checkpoint != bs[1].Block || inspected.ABIHash != ABIHash {
		t.Fatalf("incorrect inspect header %+v", inspected)
	}
	digest := fileHash(t, path)
	sidecar, err := os.ReadFile(path + ".sha256")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(sidecar), digest+"  ") {
		t.Fatal("sidecar digest mismatch")
	}
	var manifest Manifest
	b, err := os.ReadFile(filepath.Join(filepath.Dir(path), "latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.File != filepath.Base(path) || manifest.SHA256 != digest || !reflect.DeepEqual(manifest.Header, inspected) {
		t.Fatalf("invalid manifest %+v", manifest)
	}
	dest := &fakeStore{}
	if err = Restore(ctx, dest, dep, path, digest); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(dest.committed, bs) {
		t.Fatalf("restore mismatch %+v", dest.committed)
	}
}
func TestRestoreRejectsBeforeMutation(t *testing.T) {
	ctx := context.Background()
	dep, bs := fixture()
	path, err := Export(ctx, &fakeStore{batches: bs, head: &bs[1].Block}, dep, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		dep  model.Deployment
		hash string
	}{{"checksum mismatch", dep, strings.Repeat("00", 32)}, {"checksum syntax", dep, "invalid"}, {"runtime mismatch", func() model.Deployment { d := dep; d.RuntimeCodeHash = common.HexToHash("ff"); return d }(), ""}, {"start mismatch", func() model.Deployment { d := dep; d.FromBlock++; return d }(), ""}, {"chain mismatch", func() model.Deployment { d := dep; d.ChainID = "1"; return d }(), ""}, {"protocol mismatch", func() model.Deployment { d := dep; d.Protocol = common.HexToAddress("ff"); return d }(), ""}} {
		t.Run(tc.name, func(t *testing.T) {
			dest := &fakeStore{}
			if err := Restore(ctx, dest, tc.dep, path, tc.hash); err == nil {
				t.Fatal("accepted mismatching snapshot")
			}
			if dest.imports != 0 {
				t.Fatal("began database import before metadata/checksum validation")
			}
		})
	}
}
func TestCorruptStreamsRollback(t *testing.T) {
	dep, bs := fixture()
	h := header(dep)
	base := []record{{Type: "header", Header: &h}, {Type: "block", Batch: &bs[0]}, {Type: "block", Batch: &bs[1]}, {Type: "footer", Checkpoint: &bs[1].Block}}
	cases := []struct {
		name    string
		records []record
	}{{"missing footer", base[:3]}, {"trailing record", append(append([]record(nil), base...), base[2])}, {"wrong checkpoint", []record{base[0], base[1], base[2], {Type: "footer", Checkpoint: &bs[0].Block}}}, {"missing first block", []record{base[0], base[2], base[3]}}, {"duplicate block", []record{base[0], base[1], base[1], base[2], base[3]}}}
	gap := bs[1]
	gap.Block.Number++
	cases = append(cases, struct {
		name    string
		records []record
	}{"gap", []record{base[0], base[1], {Type: "block", Batch: &gap}, base[3]}})
	parent := bs[1]
	parent.Block.ParentHash = common.Hash{}
	cases = append(cases, struct {
		name    string
		records []record
	}{"parent mismatch", []record{base[0], base[1], {Type: "block", Batch: &parent}, base[3]}})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bad.gz")
			writeRecords(t, path, tc.records)
			dest := &fakeStore{}
			if err := Restore(context.Background(), dest, dep, path, ""); err == nil {
				t.Fatal("accepted corrupt stream")
			}
			if len(dest.committed) != 0 {
				t.Fatal("partial snapshot committed")
			}
			if _, err := Inspect(path); err == nil {
				t.Fatal("inspect accepted corrupt stream")
			}
		})
	}
}
func TestTruncationAndDecompressionLimits(t *testing.T) {
	dep, bs := fixture()
	path, err := Export(context.Background(), &fakeStore{batches: bs, head: &bs[1].Block}, dep, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, cut := range []int{1, 8, len(raw) / 2} {
		t.Run("truncated", func(t *testing.T) {
			bad := filepath.Join(t.TempDir(), "truncated.gz")
			if err := os.WriteFile(bad, raw[:len(raw)-cut], 0600); err != nil {
				t.Fatal(err)
			}
			dest := &fakeStore{}
			if err := Restore(context.Background(), dest, dep, bad, ""); err == nil {
				t.Fatal("accepted truncated gzip")
			}
			if len(dest.committed) != 0 {
				t.Fatal("truncated snapshot committed")
			}
		})
	}
	dest := &fakeStore{}
	if err := RestoreWithLimit(context.Background(), dest, dep, path, "", 512); err == nil {
		t.Fatal("decompression cap ignored")
	}
	if len(dest.committed) != 0 {
		t.Fatal("over-limit snapshot committed")
	}
	if _, err = InspectWithLimit(path, 512); err == nil {
		t.Fatal("inspect decompression cap ignored")
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	gz.Close()
	if _, err = InspectWithLimit(path, int64(len(plain))); err != nil {
		t.Fatalf("exact-size cap rejected: %v", err)
	}
}
func TestExportFailureNeverPublishesLatest(t *testing.T) {
	dep, bs := fixture()
	for _, source := range []*fakeStore{{batches: bs, head: &bs[0].Block}, {batches: bs, head: &bs[1].Block, exportErr: errors.New("read failed")}, {}} {
		dir := t.TempDir()
		if _, err := Export(context.Background(), source, dep, dir); err == nil {
			t.Fatal("export accepted invalid source")
		}
		var files []string
		filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				files = append(files, path)
			}
			return err
		})
		if len(files) != 0 {
			t.Fatalf("failed export left published files %v", files)
		}
	}
}
func TestSQLiteRestoreRebuildsMembership(t *testing.T) {
	ctx := context.Background()
	dep, bs := fixture()
	source, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "source.sqlite"), dep)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	for _, b := range bs {
		if err = source.Apply(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	path, err := Export(ctx, source, dep, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dest, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "dest.sqlite"), dep)
	if err != nil {
		t.Fatal(err)
	}
	defer dest.Close()
	if err = Restore(ctx, dest, dep, path, fileHash(t, path)); err != nil {
		t.Fatal(err)
	}
	head, err := dest.Checkpoint(ctx)
	if err != nil || head == nil || *head != bs[1].Block {
		t.Fatalf("checkpoint %+v: %v", head, err)
	}
	roots, err := dest.Roots(ctx, 1, 100)
	if err != nil || len(roots) != 2 {
		t.Fatalf("roots %+v: %v", roots, err)
	}
	membership, err := dest.Membership(ctx, 1, 0, roots[0].Root)
	if err != nil {
		t.Fatal(err)
	}
	if membership.Commitment != bs[1].Insertions[0].Commitments[0] || len(membership.Siblings) != 16 {
		t.Fatal("membership reconstruction failed")
	}
	spent, err := dest.IsSpent(ctx, bs[1].Nullifiers[0])
	if err != nil || !spent {
		t.Fatalf("nullifier missing %v", err)
	}
	if err = Restore(ctx, dest, dep, path, ""); err == nil {
		t.Fatal("restored over populated deployment")
	}
}

func TestPinnedABIHashMatchesSource(t *testing.T) {
	var entries []json.RawMessage
	if err := json.Unmarshal([]byte(sdkcontracts.InvisiCoreMetaData.ABI), &entries); err != nil {
		t.Fatal(err)
	}
	var events []json.RawMessage
	for _, entry := range entries {
		var item struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(entry, &item); err != nil {
			t.Fatal(err)
		}
		if item.Type == "event" {
			events = append(events, entry)
		}
	}
	b, err := json.MarshalIndent(events, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	var registryEntries []json.RawMessage
	if err := json.Unmarshal([]byte(sdkcontracts.InvisiAddressRegistryMetaData.ABI), &registryEntries); err != nil {
		t.Fatal(err)
	}
	registry, err := json.MarshalIndent(registryEntries, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	registry = append(registry, '\n')
	broadcaster, err := os.ReadFile("../protocol/BroadcasterRegistry.abi")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(append(append(b, registry...), broadcaster...))
	if hex.EncodeToString(sum[:]) != ABIHash {
		t.Fatal("snapshot ABI hash differs from pinned decoder ABI")
	}
}
func TestSQLiteCorruptFooterRollsBackAllBlocks(t *testing.T) {
	ctx := context.Background()
	dep, bs := fixture()
	h := header(dep)
	bad := filepath.Join(t.TempDir(), "bad.gz")
	writeRecords(t, bad, []record{{Type: "header", Header: &h}, {Type: "block", Batch: &bs[0]}, {Type: "block", Batch: &bs[1]}, {Type: "footer", Checkpoint: &bs[0].Block}})
	dest, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "dest.sqlite"), dep)
	if err != nil {
		t.Fatal(err)
	}
	defer dest.Close()
	if err = Restore(ctx, dest, dep, bad, ""); err == nil {
		t.Fatal("accepted incorrect footer")
	}
	head, err := dest.Checkpoint(ctx)
	if err != nil || head != nil {
		t.Fatalf("failed snapshot left checkpoint %+v: %v", head, err)
	}
	roots, err := dest.Roots(ctx, 1, 100)
	if err != nil || len(roots) != 0 {
		t.Fatalf("failed snapshot left roots %+v: %v", roots, err)
	}
	spent, err := dest.IsSpent(ctx, bs[1].Nullifiers[0])
	if err != nil || spent {
		t.Fatalf("failed snapshot left nullifier: %v", err)
	}
}

func TestTrailingPartialRecordRejected(t *testing.T) {
	dep, bs := fixture()
	h := header(dep)
	path := filepath.Join(t.TempDir(), "trailing.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	enc := json.NewEncoder(gz)
	for _, r := range []record{{Type: "header", Header: &h}, {Type: "block", Batch: &bs[0]}, {Type: "block", Batch: &bs[1]}, {Type: "footer", Checkpoint: &bs[1].Block}} {
		if err = enc.Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = gz.Write([]byte(`{"type":`)); err != nil {
		t.Fatal(err)
	}
	if err = gz.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	dest := &fakeStore{}
	if err = Restore(context.Background(), dest, dep, path, ""); err == nil {
		t.Fatal("accepted partial record after footer")
	}
	if len(dest.committed) != 0 {
		t.Fatal("partial trailing record committed")
	}
}

type cancelAfterChecks struct {
	context.Context
	cancel context.CancelFunc
	checks int
	after  int
}

func (c *cancelAfterChecks) Err() error {
	c.checks++
	if c.checks == c.after {
		c.cancel()
	}
	return c.Context.Err()
}
func TestInspectContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := InspectContext(ctx, "does-not-exist", DefaultMaxDecompressedBytes); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled inspect: %v", err)
	}
	dep, bs := fixture()
	payload := make([]byte, 256<<10)
	if _, err := rand.New(rand.NewSource(7)).Read(payload); err != nil {
		t.Fatal(err)
	}
	bs[0].Outputs = []model.NoteOutput{{Ciphertext: payload}}
	path, err := Export(context.Background(), &fakeStore{batches: bs, head: &bs[1].Block}, dep, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	during := &cancelAfterChecks{Context: base, cancel: cancel, after: 8}
	if _, err = InspectContext(during, path, DefaultMaxDecompressedBytes); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-stream cancellation ignored: %v (%d checks)", err, during.checks)
	}
}
func TestRestoreExpectedChecksumDetectsSameInodeMutation(t *testing.T) {
	dep, bs := fixture()
	payload := make([]byte, 256<<10)
	if _, err := rand.New(rand.NewSource(9)).Read(payload); err != nil {
		t.Fatal(err)
	}
	bs[0].Outputs = []model.NoteOutput{{Ciphertext: payload}}
	bs[1].Outputs = []model.NoteOutput{{Kind: "before"}}
	path, err := Export(context.Background(), &fakeStore{batches: bs, head: &bs[1].Block}, dep, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	expected := fileHash(t, path)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := bs[1]
	changed.Outputs = []model.NoteOutput{{Kind: "after"}}
	h := header(dep)
	replacement := filepath.Join(t.TempDir(), "replacement.gz")
	writeRecords(t, replacement, []record{{Type: "header", Header: &h}, {Type: "block", Batch: &bs[0]}, {Type: "block", Batch: &changed}, {Type: "footer", Checkpoint: &bs[1].Block}})
	altered, err := os.ReadFile(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original[:4096], altered[:4096]) {
		t.Fatal("mutation fixture changed the buffered prefix")
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	dest := &fakeStore{onImport: func() {
		if err := os.WriteFile(path, altered, 0600); err != nil {
			t.Fatal(err)
		}
		after, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(before, after) {
			t.Fatal("mutation fixture replaced inode")
		}
	}}
	if err = Restore(context.Background(), dest, dep, path, expected); err == nil || !strings.Contains(err.Error(), "changed after SHA256") {
		t.Fatalf("same-inode mutation not rejected by streamed checksum: %v", err)
	}
	if len(dest.committed) != 0 {
		t.Fatal("mutated snapshot committed")
	}
}
func TestRestoreKeepsVerifiedInodeAfterPathReplacement(t *testing.T) {
	dep, bs := fixture()
	path, err := Export(context.Background(), &fakeStore{batches: bs, head: &bs[1].Block}, dep, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	expected := fileHash(t, path)
	h := header(dep)
	changed := bs[1]
	changed.Outputs = []model.NoteOutput{{Kind: "unverified replacement"}}
	replacement := filepath.Join(filepath.Dir(path), "replacement.gz")
	writeRecords(t, replacement, []record{{Type: "header", Header: &h}, {Type: "block", Batch: &bs[0]}, {Type: "block", Batch: &changed}, {Type: "footer", Checkpoint: &bs[1].Block}})
	dest := &fakeStore{onImport: func() {
		if err := os.Rename(replacement, path); err != nil {
			t.Fatal(err)
		}
	}}
	if err = Restore(context.Background(), dest, dep, path, expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(dest.committed, bs) {
		t.Fatal("restored replacement instead of already-verified inode")
	}
}

func TestCreditTimestampSnapshotRoundTrip(t *testing.T) {
	ctx := context.Background()
	dep, bs := fixture()
	for i := range bs {
		bs[i].Block.Timestamp = 2678400 + uint64(i)
		bs[i].Events = []model.Event{{Name: "CreditDraw"}}
		bs[i].BindBlockContext()
	}
	last := bs[len(bs)-1].Block
	source := &fakeStore{batches: bs, head: &last}
	path, err := Export(ctx, source, dep, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dest, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "credit-time.sqlite"), dep)
	if err != nil {
		t.Fatal(err)
	}
	defer dest.Close()
	if err = Restore(ctx, dest, dep, path, ""); err != nil {
		t.Fatal(err)
	}
	events, err := dest.Events(ctx, dep.FromBlock, last.Number, 10)
	if err != nil || len(events) != len(bs) {
		t.Fatalf("events %v %v", events, err)
	}
	for i, e := range events {
		if e.Block != bs[i].Block {
			t.Fatalf("timestamp/context lost: %+v", e)
		}
	}
	cp, err := dest.Checkpoint(ctx)
	if err != nil || *cp != last {
		t.Fatalf("checkpoint %v %v", cp, err)
	}
}

func TestHeaderDoesNotBindSourceCommit(t *testing.T) {
	dep, _ := fixture()
	h := header(dep)
	raw, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["sourceCommit"]; exists {
		t.Fatal("snapshot compatibility must not depend on a source commit")
	}
	if err := checkHeader(h, &dep); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"format", "profile", "abi", "deployment"} {
		changed := h
		switch kind {
		case "format":
			changed.Version++
		case "profile":
			changed.SourceProfile = "unsupported"
		case "abi":
			changed.ABIHash = strings.Repeat("0", 64)
		case "deployment":
			changed.Deployment.ChainID = "999999"
		}
		if err := checkHeader(changed, &dep); err == nil {
			t.Fatalf("accepted incompatible %s", kind)
		}
	}
}

func TestRegistrySnapshotRoundTripAndPins(t *testing.T) {
	ctx := context.Background()
	dep := model.Deployment{ChainID: "97", Protocol: common.HexToAddress("0x1"), FromBlock: 10, Registry: &model.ContractDeployment{Address: common.HexToAddress("0x2"), RuntimeCodeHash: common.HexToHash("0x3"), FromBlock: 8}}
	source, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "source.db"), dep)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	owner := common.HexToAddress("0x1234")
	for n := uint64(8); n <= 10; n++ {
		b := model.Batch{Block: model.Block{Number: n, Hash: common.BigToHash(new(big.Int).SetUint64(n)), ParentHash: common.BigToHash(new(big.Int).SetUint64(n - 1))}}
		if n == 8 || n == 10 {
			b.Registrations = []model.RegistrationUpdate{{WalletIdentity: owner, ViewingPublicKey: []byte{2}, ClueKey: common.HexToHash("0x1"), Nonce: n, LogIndex: 1}}
		}
		if err = source.Apply(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	path, err := Export(ctx, source, dep, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	destination, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "destination.db"), dep)
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	if err = Restore(ctx, destination, dep, path, fileHash(t, path)); err != nil {
		t.Fatal(err)
	}
	got, err := destination.ReceivingAddress(ctx, owner)
	if err != nil || got == nil || got.Nonce != 10 {
		t.Fatalf("restored: %+v %v", got, err)
	}
	if err = destination.Rollback(ctx, 9); err != nil {
		t.Fatal(err)
	}
	got, err = destination.ReceivingAddress(ctx, owner)
	if err != nil || got == nil || got.Nonce != 8 {
		t.Fatalf("restored rollback: %+v %v", got, err)
	}
	changed := dep
	changed.Registry = &model.ContractDeployment{Address: common.HexToAddress("0x4"), RuntimeCodeHash: dep.Registry.RuntimeCodeHash, FromBlock: 8}
	if err = Restore(ctx, destination, changed, path, fileHash(t, path)); err == nil {
		t.Fatal("accepted mismatched registry")
	}
}

func TestBroadcasterSnapshotRoundTripAndPins(t *testing.T) {
	ctx := context.Background()
	dep := model.Deployment{ChainID: "97", Protocol: common.HexToAddress("0x1"), FromBlock: 10, BroadcasterRegistry: &model.ContractDeployment{Address: common.HexToAddress("0x2"), RuntimeCodeHash: common.HexToHash("0x3"), FromBlock: 8}}
	source, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "source.db"), dep)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	owner := common.HexToAddress("0x1234")
	on, off := true, false
	for n := uint64(8); n <= 10; n++ {
		b := model.Batch{Block: model.Block{Number: n, Hash: common.BigToHash(new(big.Int).SetUint64(n)), ParentHash: common.BigToHash(new(big.Int).SetUint64(n - 1))}}
		if n == 8 {
			b.Broadcasters = []model.BroadcasterUpdate{{Operator: owner, Endpoint: "https://relay.example", QuoteSigner: owner, FeeRecipient: owner, LogIndex: 1}, {Operator: owner, Enabled: &on, LogIndex: 2}}
		}
		if n == 10 {
			b.Broadcasters = []model.BroadcasterUpdate{{Operator: owner, Enabled: &off, LogIndex: 1}}
		}
		if err = source.Apply(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	path, err := Export(ctx, source, dep, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dest, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "dest.db"), dep)
	if err != nil {
		t.Fatal(err)
	}
	defer dest.Close()
	if err = Restore(ctx, dest, dep, path, fileHash(t, path)); err != nil {
		t.Fatal(err)
	}
	page, err := dest.Broadcasters(ctx, model.BroadcasterQuery{Limit: 10})
	if err != nil || len(page.Broadcasters) != 1 || page.Broadcasters[0].Enabled {
		t.Fatalf("restored %+v %v", page, err)
	}
	if err = dest.Rollback(ctx, 9); err != nil {
		t.Fatal(err)
	}
	page, err = dest.Broadcasters(ctx, model.BroadcasterQuery{Limit: 10})
	if err != nil || !page.Broadcasters[0].Enabled {
		t.Fatalf("rollback %+v %v", page, err)
	}
	changed := dep
	changed.BroadcasterRegistry = nil
	if err = Restore(ctx, dest, changed, path, fileHash(t, path)); err == nil {
		t.Fatal("accepted omitted broadcaster pins")
	}
}

func TestMetadataOnlyCoreRenameKeepsExistingSnapshotHeaders(t *testing.T) {
	dep, _ := fixture()
	h := header(dep)
	h.ABIHash = metadataEquivalentABIHash
	if err := checkHeader(h, &dep); err != nil {
		t.Fatal(err)
	}
	h.ABIHash = strings.Repeat("f", 64)
	if err := checkHeader(h, &dep); err == nil {
		t.Fatal("unreviewed ABI fingerprint accepted")
	}
}

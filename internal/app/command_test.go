package app

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ethereum/go-ethereum/common"
	deployment "github.com/invisi-lab/invisi-protocol-rpc"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/config"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/snapshot"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/storage"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cliDeployment() model.Deployment {
	return model.Deployment{ChainID: "31337", Protocol: common.HexToAddress("0x1"), FromBlock: 10, RuntimeCodeHash: common.HexToHash("0x2")}
}
func privateDeployments() []model.Deployment {
	first := cliDeployment()
	second := first
	second.ChainID = "31338"
	second.Protocol = common.HexToAddress("0x3")
	second.RuntimeCodeHash = common.HexToHash("0x4")
	return []model.Deployment{first, second}
}
func privateCatalog(t *testing.T, deps []model.Deployment) string {
	t.Helper()
	catalog := deployment.Catalog{Version: 1, Chains: map[string]deployment.Profile{}}
	for _, dep := range deps {
		catalog.Chains[dep.ChainID] = deployment.Profile{Name: "private " + dep.ChainID, DeploymentProfile: dep.Profile()}
	}
	raw, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "deployment.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func privateInitArgs(dir, catalog string, ids ...string) []string {
	return []string{"invisi-protocol-rpc", "chain", "add", ids[0], "--dir", dir, "--deployment-file", catalog, "--rpc", "http://127.0.0.1:8545"}
}
func runCommand(t *testing.T, args ...string) string {
	t.Helper()
	var output bytes.Buffer
	if err := Command(&output).Run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	return output.String()
}
func commandSnapshot(t *testing.T, dep model.Deployment) string {
	t.Helper()
	source, err := storage.OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "source.sqlite"), dep)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	first := model.Batch{Block: model.Block{Number: dep.FromBlock, Hash: common.HexToHash("0x10")}, CreatedTrees: []uint64{1}, Insertions: []model.Insertion{{TreeID: 1, Commitments: []common.Hash{common.HexToHash("0x1")}}}}
	if err = source.Apply(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	path, err := snapshot.Export(context.Background(), source, dep, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}
func duplicateTreeSnapshot(t *testing.T, source string) string {
	t.Helper()
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	var encoded bytes.Buffer
	writer := gzip.NewWriter(&encoded)
	decode := json.NewDecoder(gz)
	encode := json.NewEncoder(writer)
	for {
		var record map[string]json.RawMessage
		err = decode.Decode(&record)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if string(record["type"]) == `"block"` {
			var batch model.Batch
			if err = json.Unmarshal(record["batch"], &batch); err != nil {
				t.Fatal(err)
			}
			batch.CreatedTrees = []uint64{1, 1}
			record["batch"], err = json.Marshal(batch)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err = encode.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "duplicate-tree.snapshot.gz")
	if err = os.WriteFile(path, encoded.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded.Bytes())
	if err = os.WriteFile(path+".sha256", []byte(hex.EncodeToString(digest[:])+"  snapshot\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = snapshot.Inspect(path); err != nil {
		t.Fatalf("regression fixture must pass structural validation: %v", err)
	}
	return path
}

func initDirectory(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "node")
	runCommand(t, "invisi-protocol-rpc", "init", "--dir", dir)
	return dir
}
func TestCommandChainLifecycle(t *testing.T) {
	dir := initDirectory(t)
	cfg, e := config.LoadDir(dir)
	if e != nil || len(cfg.Chains) != 0 {
		t.Fatalf("empty init: %+v %v", cfg, e)
	}
	before, _ := os.ReadFile(config.File(dir))
	if e = Command(io.Discard).Run(context.Background(), []string{"invisi-protocol-rpc", "init", "--dir", dir}); e == nil {
		t.Fatal("overwrite accepted")
	}
	after, _ := os.ReadFile(config.File(dir))
	if !bytes.Equal(before, after) {
		t.Fatal("overwritten")
	}
	catalog := privateCatalog(t, privateDeployments())
	for _, id := range []string{"31337", "31338"} {
		runCommand(t, privateInitArgs(dir, catalog, id)...)
	}
	args := privateInitArgs(dir, catalog, "31337")
	if e = Command(io.Discard).Run(context.Background(), args); e == nil {
		t.Fatal("duplicate accepted")
	}
	runCommand(t, "invisi-protocol-rpc", "chain", "set-rpc", "31337", "--dir", dir, "--rpc", "https://user:password@example.com/token?secret=value")
	list := runCommand(t, "invisi-protocol-rpc", "chain", "list", "--dir", dir)
	if strings.Contains(list, "password") || strings.Contains(list, "secret") || strings.Contains(list, "token") {
		t.Fatal(list)
	}
	cfg, _ = config.LoadDir(dir)
	db := cfg.Chains[0].Database.Path
	runCommand(t, "invisi-protocol-rpc", "chain", "remove", "31337", "--dir", dir)
	if _, e = os.Stat(db); e != nil {
		t.Fatal("remove deleted DB", e)
	}
	runCommand(t, args...)
	for _, verb := range []string{"remove", "set-rpc"} {
		args := []string{"invisi-protocol-rpc", "chain", verb, "999", "--dir", dir}
		if verb == "set-rpc" {
			args = append(args, "--rpc", "http://localhost")
		}
		if Command(io.Discard).Run(context.Background(), args) == nil {
			t.Fatal("unknown accepted")
		}
	}
}
func TestCommandInvalidSelectionsBeforeDatabaseChanges(t *testing.T) {
	for _, test := range []struct {
		id    string
		extra []string
		want  string
	}{
		{"031337", nil, "canonical"}, {"999", nil, "999"}, {"31337", []string{"--rpc", "file:///tmp/test"}, "--rpc"}, {"31337", []string{"--rpc", "https:///"}, "--rpc"}, {"31337", []string{"--snapshot-sha256", strings.Repeat("0", 64)}, "requires --snapshot"}, {"31337", []string{"--snapshot-max-bytes", "0"}, "must be positive"}, {"31337", []string{"--database-url", "postgres://localhost/test"}, "requires --database postgres"},
	} {
		t.Run(test.want+test.id, func(t *testing.T) {
			dir := initDirectory(t)
			before, _ := os.ReadFile(config.File(dir))
			args := privateInitArgs(dir, privateCatalog(t, privateDeployments()), test.id)
			args = append(args, test.extra...)
			e := Command(io.Discard).Run(context.Background(), args)
			if e == nil || !strings.Contains(e.Error(), test.want) {
				t.Fatalf("wanted %s: %v", test.want, e)
			}
			after, _ := os.ReadFile(config.File(dir))
			if !bytes.Equal(before, after) {
				t.Fatal("failed add changed config")
			}
			if _, e = os.Stat(filepath.Join(dir, "chains")); !os.IsNotExist(e) {
				t.Fatal("invalid add opened DB")
			}
		})
	}
	for _, flag := range []string{"chain", "deployment-file", "database", "snapshot", "confirmations", "snapshot-interval"} {
		dir := filepath.Join(t.TempDir(), "bad")
		if Command(io.Discard).Run(context.Background(), []string{"invisi-protocol-rpc", "init", "--dir", dir, "--" + flag, "value"}) == nil {
			t.Fatal("init accepted", flag)
		}
	}
}
func TestCommandSnapshotsBoundToCatalogAndPrevalidated(t *testing.T) {
	deps := privateDeployments()
	catalog := privateCatalog(t, deps)
	first, second := commandSnapshot(t, deps[0]), commandSnapshot(t, deps[1])
	for _, test := range []struct {
		name, source string
		extra        []string
		want         string
	}{{"binding", second, nil, "differs from deployment catalog"}, {"checksum", first, []string{"--snapshot-sha256", strings.Repeat("0", 64)}, "SHA256 mismatch"}, {"limit", first, []string{"--snapshot-max-bytes", "1"}, "decompressed byte limit"}} {
		t.Run(test.name, func(t *testing.T) {
			dir := initDirectory(t)
			before, _ := os.ReadFile(config.File(dir))
			args := append(privateInitArgs(dir, catalog, "31337"), "--snapshot", test.source)
			args = append(args, test.extra...)
			e := Command(io.Discard).Run(context.Background(), args)
			if e == nil || !strings.Contains(e.Error(), test.want) {
				t.Fatalf("%v", e)
			}
			after, _ := os.ReadFile(config.File(dir))
			if !bytes.Equal(before, after) {
				t.Fatal("changed config")
			}
			if _, e = os.Stat(filepath.Join(dir, "chains")); !os.IsNotExist(e) {
				t.Fatal("opened DB before validation")
			}
		})
	}
	dir := initDirectory(t)
	args := append(privateInitArgs(dir, catalog, "31337"), "--snapshot", first)
	runCommand(t, args...)
	cfg, _ := config.LoadDir(dir)
	store, e := OpenDatabase(context.Background(), cfg.Chains[0])
	if e != nil {
		t.Fatal(e)
	}
	head, e := store.Checkpoint(context.Background())
	store.Close()
	if e != nil || head == nil || head.Number != 10 {
		t.Fatal(head, e)
	}
	runCommand(t, "invisi-protocol-rpc", "chain", "remove", "31337", "--dir", dir)
	if output := runCommand(t, args...); !strings.Contains(output, "already restored") {
		t.Fatal(output)
	}
	runCommand(t, "invisi-protocol-rpc", "chain", "remove", "31337", "--dir", dir)
	other := privateDeployments()[0]
	other.RuntimeCodeHash = common.HexToHash("0x123")
	args = privateInitArgs(dir, privateCatalog(t, []model.Deployment{other}), "31337")
	if Command(io.Discard).Run(context.Background(), args) == nil {
		t.Fatal("rebound existing DB")
	}
}
func TestCommandSnapshotPopulatedTargetPreservesData(t *testing.T) {
	dep := cliDeployment()
	dir := initDirectory(t)
	target := filepath.Join(dir, "chains", dep.ChainID, "protocol.sqlite")
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	store, e := storage.OpenSQLite(context.Background(), target, dep)
	if e != nil {
		t.Fatal(e)
	}
	batch := model.Batch{Block: model.Block{Number: 10, Hash: common.HexToHash("0x999")}}
	if e = store.Apply(context.Background(), batch); e != nil {
		t.Fatal(e)
	}
	store.Close()
	before, _ := os.ReadFile(config.File(dir))
	args := append(privateInitArgs(dir, privateCatalog(t, []model.Deployment{dep}), dep.ChainID), "--snapshot", commandSnapshot(t, dep))
	if e = Command(io.Discard).Run(context.Background(), args); e == nil || !strings.Contains(e.Error(), "is not empty") {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(config.File(dir))
	if !bytes.Equal(before, after) {
		t.Fatal("changed config")
	}
	store, e = storage.OpenSQLite(context.Background(), target, dep)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	head, e := store.Checkpoint(context.Background())
	if e != nil || head == nil || *head != batch.Block {
		t.Fatal(head, e)
	}
}
func TestCommandRetryAfterSnapshotReplayFailure(t *testing.T) {
	dep := cliDeployment()
	valid := commandSnapshot(t, dep)
	broken := duplicateTreeSnapshot(t, valid)
	dir := initDirectory(t)
	before, _ := os.ReadFile(config.File(dir))
	args := append(privateInitArgs(dir, privateCatalog(t, []model.Deployment{dep}), dep.ChainID), "--snapshot", broken)
	if e := Command(io.Discard).Run(context.Background(), args); e == nil || !strings.Contains(e.Error(), "restore snapshot") {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(config.File(dir))
	if !bytes.Equal(before, after) {
		t.Fatal("failed restore published")
	}
	store, e := storage.OpenSQLite(context.Background(), filepath.Join(dir, "chains", dep.ChainID, "protocol.sqlite"), dep)
	if e != nil {
		t.Fatal(e)
	}
	head, e := store.Checkpoint(context.Background())
	store.Close()
	if e != nil || head != nil {
		t.Fatal("partial import", head, e)
	}
	for _, suffix := range []string{"", ".sha256"} {
		raw, e := os.ReadFile(valid + suffix)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(broken+suffix, raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	runCommand(t, args...)
}
func TestCommandSnapshotExportRequiresSelectedChain(t *testing.T) {
	dir := initDirectory(t)
	base := []string{"invisi-protocol-rpc", "snapshot", "export", "--dir", dir}
	if e := Command(io.Discard).Run(context.Background(), base); e == nil || !strings.Contains(e.Error(), "configured chain") {
		t.Fatal(e)
	}
	deps := privateDeployments()
	catalog := privateCatalog(t, deps)
	runCommand(t, append(privateInitArgs(dir, catalog, "31337"), "--snapshot", commandSnapshot(t, deps[0]))...)
	path := strings.TrimSpace(runCommand(t, base...))
	if _, e := os.Stat(path); e != nil {
		t.Fatal(e)
	}
	runCommand(t, privateInitArgs(dir, catalog, "31338")...)
	if e := Command(io.Discard).Run(context.Background(), base); e == nil || !strings.Contains(e.Error(), "requires --chain") {
		t.Fatal(e)
	}
	if e := Command(io.Discard).Run(context.Background(), append(base, "--chain", "999")); e == nil {
		t.Fatal("unknown export")
	}
	output := runCommand(t, "invisi-protocol-rpc", "snapshot", "inspect", path)
	var h snapshot.Header
	if e := json.Unmarshal([]byte(output), &h); e != nil || h.Deployment != deps[0] {
		t.Fatal(e, h)
	}
	if Command(io.Discard).Run(context.Background(), []string{"invisi-protocol-rpc", "snapshot", "inspect", "--max-bytes", "1", path}) == nil {
		t.Fatal("ignored inspect limit")
	}
}
func TestCommandHelp(t *testing.T) {
	for _, path := range [][]string{{}, {"init"}, {"run"}, {"chain"}, {"chain", "add"}, {"chain", "list"}, {"snapshot", "export"}, {"snapshot", "inspect"}} {
		args := append([]string{"invisi-protocol-rpc"}, path...)
		if output := runCommand(t, append(args, "--help")...); !strings.Contains(output, "USAGE:") {
			t.Fatal(output)
		}
	}
}

func TestCommandAddRequiresSelectedDeploymentFile(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("INVISI_RPC_DEPLOYMENT_FILE", "")
	dir := initDirectory(t)
	before, err := os.ReadFile(config.File(dir))
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"invisi-protocol-rpc", "chain", "add", "31337", "--dir", dir, "--rpc", "http://localhost:8545"}
	if err = Command(io.Discard).Run(context.Background(), args); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing deployment file accepted: %v", err)
	}
	raw, err := os.ReadFile(privateCatalog(t, privateDeployments()))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile("deployment.json", raw, 0600); err != nil {
		t.Fatal(err)
	}
	missing := append([]string(nil), args...)
	missing[3] = "42161"
	if err = Command(io.Discard).Run(context.Background(), missing); err == nil || !strings.Contains(err.Error(), "no deployment profile") {
		t.Fatalf("unlisted chain accepted: %v", err)
	}
	after, err := os.ReadFile(config.File(dir))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed add changed config: %v", err)
	}
	runCommand(t, args...)
}

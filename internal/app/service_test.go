package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	gethrpc "github.com/ethereum/go-ethereum/rpc"
	"github.com/invisi-labs/invisi-rpc/internal/config"
	"github.com/invisi-labs/invisi-rpc/internal/iplimit"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	apirpc "github.com/invisi-labs/invisi-rpc/internal/rpc"
	"github.com/invisi-labs/invisi-rpc/internal/storage"
)

// chainRPCFixture exposes actual Ethereum JSON-RPC wire responses, allowing the
// service preflight and lifecycle to run without an archive node or local miner.
type chainRPCFixture struct {
	latest                uint64
	chainID               int64
	advanceTo             atomic.Uint64
	protocol              common.Address
	awaitLatest           chan struct{}
	missing               string
	logsError, stateError bool
	logsCalls, stateCalls atomic.Int64
	rootMissing           bool
	rootBlock             json.RawMessage
	versionCalls          atomic.Int64
	chainIDCalls          atomic.Int64
	versionError          bool
	versionResult         []byte
}

func (f *chainRPCFixture) GetBlockByNumber(ctx context.Context, number string, _ bool) (*types.Header, error) {
	if number == "latest" && f.awaitLatest != nil {
		select {
		case f.awaitLatest <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if number == f.missing {
		return nil, errors.New("historical block pruned")
	}
	height := f.latest
	if v := f.advanceTo.Load(); v > height {
		height = v
	}
	if number != "latest" {
		parsed, err := hexutil.DecodeUint64(number)
		if err != nil {
			return nil, err
		}
		height = parsed
	}
	var header *types.Header
	for n := uint64(0); n <= height; n++ {
		parent := common.Hash{}
		if header != nil {
			parent = header.Hash()
		}
		header = &types.Header{ParentHash: parent, Number: new(big.Int).SetUint64(n), Difficulty: big.NewInt(1), GasLimit: 30_000_000, Time: 1000 + n, Extra: []byte(fmt.Sprint(f.chainID))}
	}
	return header, nil
}
func (f *chainRPCFixture) GetLogs(context.Context, map[string]any) ([]types.Log, error) {
	f.logsCalls.Add(1)
	if f.logsError {
		return nil, errors.New("historical logs pruned")
	}
	return []types.Log{}, nil
}
func (f *chainRPCFixture) GetCode(_ context.Context, address common.Address, _ string) (hexutil.Bytes, error) {
	if f.protocol != (common.Address{}) && address != f.protocol {
		return nil, errors.New("wrong deployment RPC provider")
	}
	f.stateCalls.Add(1)
	if f.stateError {
		return nil, errors.New("historical state pruned")
	}
	return hexutil.Bytes{0x60, 0x00}, nil
}
func (f *chainRPCFixture) ChainId(context.Context) (hexutil.Big, error) {
	f.chainIDCalls.Add(1)
	id := f.chainID
	if id == 0 {
		id = 31337
	}
	return hexutil.Big(*big.NewInt(id)), nil
}
func fixtureChain(t *testing.T, f *chainRPCFixture) (*ethclient.Client, string) {
	t.Helper()
	server := gethrpc.NewServer()
	if err := server.RegisterName("eth", f); err != nil {
		t.Fatal(err)
	}
	http := httptest.NewServer(server)
	t.Cleanup(http.Close)
	client, err := ethclient.Dial(http.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client, http.URL
}
func serviceConfig(t *testing.T) config.Chain {
	t.Helper()
	cfg := config.Chain{ChainRPC: "http://127.0.0.1:8545", Deployment: model.Deployment{ChainID: "31337", Protocol: common.HexToAddress("0x1"), FromBlock: 10}, Database: config.Database{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "service.sqlite")}, SnapshotInterval: "0s", PollSeconds: 1}
	cfg.Deployment.RuntimeCodeHash = crypto.Keccak256Hash([]byte{0x60, 0x00})
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return cfg
}
func appendThrough(t *testing.T, s storage.Backend, from, to uint64) {
	t.Helper()
	var parent common.Hash
	for n := from; n <= to; n++ {
		b := model.Batch{Block: model.Block{Number: n, Hash: common.BigToHash(new(big.Int).SetUint64(n + 1)), ParentHash: parent}}
		if err := s.Apply(context.Background(), b); err != nil {
			t.Fatal(err)
		}
		parent = b.Block.Hash
	}
}
func TestCheckAvailability(t *testing.T) {
	cases := []struct {
		name             string
		fixture          chainRPCFixture
		checkpoint, from uint64
		want             string
		noArchive        bool
	}{
		{name: "initial block available", fixture: chainRPCFixture{latest: 15}, from: 10},
		{name: "required height pruned", fixture: chainRPCFixture{latest: 15, missing: "0xa"}, from: 10, want: "required block height 10"},
		{name: "required logs pruned", fixture: chainRPCFixture{latest: 15, logsError: true}, from: 10, want: "logs at required height 10"},
		{name: "required state pruned", fixture: chainRPCFixture{latest: 15, stateError: true}, from: 10, want: "historical state at required height 10"},
		{name: "caught up is normal", fixture: chainRPCFixture{latest: 15, logsError: true, stateError: true}, checkpoint: 15, from: 10, noArchive: true},
		{name: "upstream behind checkpoint", fixture: chainRPCFixture{latest: 15}, checkpoint: 16, from: 10, want: "behind saved checkpoint height 16"},
		{name: "starting block in future", fixture: chainRPCFixture{latest: 15}, from: 20, want: "precedes required starting height 20"},
		{name: "saved checkpoint header pruned", fixture: chainRPCFixture{latest: 15, missing: "0xa"}, checkpoint: 10, from: 10, want: "saved checkpoint height 10"},
		{name: "resume next height", fixture: chainRPCFixture{latest: 15}, checkpoint: 10, from: 10},
	}
	for n := range cases {
		test := &cases[n]
		t.Run(test.name, func(t *testing.T) {
			cfg := serviceConfig(t)
			cfg.Deployment.FromBlock = test.from
			store, err := OpenDatabase(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if test.checkpoint > 0 {
				appendThrough(t, store, test.from, test.checkpoint)
			}
			chain, url := fixtureChain(t, &test.fixture)
			cfg.ChainRPC = url
			err = CheckAvailability(context.Background(), chain, store, cfg)
			if test.want == "" && err != nil {
				t.Fatal(err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("wanted %q, got %v", test.want, err)
			}
			if test.noArchive && (test.fixture.logsCalls.Load() != 0 || test.fixture.stateCalls.Load() != 0) {
				t.Fatal("caught-up preflight requested unneeded archive data")
			}
		})
	}
}
func TestRunCancellationPersistsCheckpointAndReleasesListener(t *testing.T) {
	fixture := &chainRPCFixture{latest: 0}
	_, chainURL := fixtureChain(t, fixture)
	cfg := serviceConfig(t)
	cfg.ChainRPC = chainURL
	cfg.Deployment.FromBlock = 0
	cfg.Deployment.RuntimeCodeHash = crypto.Keccak256Hash([]byte{0x60, 0x00})
	socket, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := socket.Addr().String()
	socket.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, config.Config{Listen: listen, Chains: []config.Chain{cfg}}, t.TempDir()) }()
	var status apirpc.Status
	deadline := time.Now().Add(5 * time.Second)
	for {
		client, e := gethrpc.DialHTTP("http://" + listen + "/v1/chains/" + cfg.Deployment.ChainID)
		if e == nil {
			poll, c := context.WithTimeout(context.Background(), 100*time.Millisecond)
			e = client.CallContext(poll, &status, "invisi_getStatus")
			c()
			client.Close()
			if e == nil && status.IndexedHead != nil {
				break
			}
		}
		select {
		case e := <-done:
			t.Fatalf("service exited before indexing: %v", e)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("service did not publish indexed checkpoint")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status.IndexedHead.Number != 0 {
		t.Fatalf("unexpected indexed checkpoint: %+v", status.IndexedHead)
	}
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("service did not stop on cancellation")
	}
	reopened, err := OpenDatabase(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	head, err := reopened.Checkpoint(context.Background())
	if err != nil || head == nil || head.Number != 0 {
		t.Fatalf("checkpoint not persisted: %v %v", head, err)
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		t.Fatalf("service listener remained open: %v", err)
	}
	listener.Close()
	if got := fixture.logsCalls.Load(); got < 2 {
		t.Fatal(fmt.Sprintf("expected real preflight and indexing log calls, got %d", got))
	}
}

func (f *chainRPCFixture) Call(_ context.Context, args map[string]json.RawMessage, block json.RawMessage) (hexutil.Bytes, error) {
	var input string
	raw := args["input"]
	if len(raw) == 0 {
		raw = args["data"]
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, err
		}
	}
	if input == hexutil.Encode(crypto.Keccak256([]byte("VERSION()"))[:4]) {
		f.versionCalls.Add(1)
		if string(block) != `"latest"` {
			return nil, errors.New("VERSION() must use latest state")
		}
		if f.versionError {
			return nil, errors.New("VERSION() RPC unavailable")
		}
		if f.versionResult != nil {
			return append(hexutil.Bytes(nil), f.versionResult...), nil
		}
		result := make([]byte, 32)
		result[31] = 1
		return result, nil
	}
	f.rootBlock = append(json.RawMessage(nil), block...)
	result := make([]byte, 32)
	if !f.rootMissing {
		result[31] = 1
	}
	return result, nil
}
func TestVerifySavedStateCanonicalCheckpointAndRoots(t *testing.T) {
	cfg := serviceConfig(t)
	fixture := &chainRPCFixture{latest: 11}
	chain, url := fixtureChain(t, fixture)
	cfg.ChainRPC = url
	store, err := OpenDatabase(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	firstHeader, err := fixture.GetBlockByNumber(context.Background(), "0xa", false)
	if err != nil {
		t.Fatal(err)
	}
	first := model.Batch{Block: model.Block{Number: 10, Timestamp: firstHeader.Time, Hash: firstHeader.Hash()}, CreatedTrees: []uint64{1}, Insertions: []model.Insertion{{TreeID: 1, Commitments: []common.Hash{common.HexToHash("0x1")}}}}
	if err = store.Apply(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	secondHeader, err := fixture.GetBlockByNumber(context.Background(), "0xb", false)
	if err != nil {
		t.Fatal(err)
	}
	second := model.Batch{Block: model.Block{Number: 11, Timestamp: secondHeader.Time, Hash: secondHeader.Hash(), ParentHash: first.Block.Hash}}
	if err = store.Apply(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err = VerifySavedState(context.Background(), chain, store, cfg); err != nil {
		t.Fatal(err)
	}
	var block struct {
		BlockHash        common.Hash `json:"blockHash"`
		RequireCanonical bool        `json:"requireCanonical"`
	}
	if err = json.Unmarshal(fixture.rootBlock, &block); err != nil {
		t.Fatal(err)
	}
	if block.BlockHash != second.Block.Hash {
		t.Fatalf("saved root not validated at canonical checkpoint: %s", fixture.rootBlock)
	}
	fixture.rootMissing = true
	if err = VerifySavedState(context.Background(), chain, store, cfg); err == nil || !strings.Contains(err.Error(), "cannot validate saved tree 1 at checkpoint height 11") {
		t.Fatalf("unrecorded restored root accepted: %v", err)
	}
	fixture.missing = "0xb"
	if err = VerifySavedState(context.Background(), chain, store, cfg); err == nil || !strings.Contains(err.Error(), "checkpoint height 11") {
		t.Fatalf("missing checkpoint header accepted: %v", err)
	}
	fixture.missing = ""
	if err = store.Rollback(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	second.Block.Hash = common.HexToHash("0xbad")
	if err = store.Apply(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err = VerifySavedState(context.Background(), chain, store, cfg); err == nil || !strings.Contains(err.Error(), "not canonical") {
		t.Fatalf("noncanonical restored checkpoint accepted: %v", err)
	}
}

func unusedListenAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}
func launchService(t *testing.T, c config.Config, dir string) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	exited := make(chan struct{})
	go func() { defer close(exited); done <- Run(ctx, c, dir) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Error("service cleanup did not finish")
		}
	})
	return cancel, done
}
func waitChainStatus(t *testing.T, listen, chainID string, minHead uint64, done <-chan error) apirpc.Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		client, err := gethrpc.DialHTTP("http://" + listen + "/v1/chains/" + chainID)
		var status apirpc.Status
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			err = client.CallContext(ctx, &status, "invisi_getStatus")
			cancel()
			client.Close()
			if err == nil && status.IndexedHead != nil && status.IndexedHead.Number >= minHead {
				return status
			}
		}
		select {
		case err := <-done:
			t.Fatalf("service exited before chain %s ready: %v", chainID, err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("chain %s did not reach checkpoint %d: %v", chainID, minHead, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func twoChainService(t *testing.T) (config.Config, *chainRPCFixture, *chainRPCFixture) {
	t.Helper()
	first := serviceConfig(t)
	first.Deployment.ChainID = "31337"
	first.Deployment.Protocol = common.HexToAddress("31337")
	first.Deployment.FromBlock = 0
	first.SnapshotInterval = "20ms"
	second := serviceConfig(t)
	second.Deployment.ChainID = "31338"
	second.Deployment.Protocol = common.HexToAddress("31338")
	second.Deployment.FromBlock = 0
	second.SnapshotInterval = "20ms"
	a := &chainRPCFixture{chainID: 31337, protocol: first.Deployment.Protocol}
	b := &chainRPCFixture{chainID: 31338, protocol: second.Deployment.Protocol}
	_, first.ChainRPC = fixtureChain(t, a)
	_, second.ChainRPC = fixtureChain(t, b)
	// Lifecycle fixtures deliberately poll every 10ms; use an explicit positive
	// test quota so they exercise reload/indexing rather than exhaust the public default.
	testRate := 10000
	return config.Config{Listen: unusedListenAddress(t), RateLimit: &iplimit.Config{RequestsPerMinute: &testRate}, Chains: []config.Chain{second, first}}, a, b
}
func TestRunMultipleChainsRoutesCheckpointsSnapshotsAndRestart(t *testing.T) {
	cfg, a, _ := twoChainService(t)
	dir := t.TempDir()
	cancel, done := launchService(t, cfg, dir)
	for _, chain := range cfg.Chains {
		status := waitChainStatus(t, cfg.Listen, chain.Deployment.ChainID, 0, done)
		if status.APIVersion != "v1" || status.ProtocolVersion != "1" {
			t.Fatalf("missing API or Protocol version: %+v", status.VersionInfo)
		}
		if status.Deployment != chain.Deployment {
			t.Fatalf("chain route exposed wrong deployment: %+v", status.Deployment)
		}
	}
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get("http://" + cfg.Listen + "/v1")
	if err != nil {
		t.Fatal(err)
	}
	var discovery struct {
		APIVersion string           `json:"apiVersion"`
		Chains     []discoveryChain `json:"chains"`
	}
	err = json.NewDecoder(resp.Body).Decode(&discovery)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || discovery.APIVersion != "v1" || len(discovery.Chains) != 2 || discovery.Chains[0].ChainID != "31338" || discovery.Chains[1].ChainID != "31337" || discovery.Chains[0].RPCPath != "/v1/chains/31338" || discovery.Chains[0].ProtocolVersion != "1" || discovery.Chains[1].ProtocolVersion != "1" {
		t.Fatalf("unstable discovery response %+v", discovery)
	}
	for _, path := range []string{"/", "/v999", "/chains/31337", "/v1/chains/999", "/v1/chains/31337/extra", "/v1/chains/31337//"} {
		r, err := client.Get("http://" + cfg.Listen + path)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusNotFound {
			t.Errorf("unexpected route %s: %d", path, r.StatusCode)
		}
	}
	root, err := gethrpc.DialHTTP("http://" + cfg.Listen + "/")
	if err != nil {
		t.Fatal(err)
	}
	var ignored any
	err = root.Call(&ignored, "invisi_getStatus")
	root.Close()
	if err == nil {
		t.Fatal("unversioned root unexpectedly accepts RPC")
	}
	trailing, err := gethrpc.DialHTTP("http://" + cfg.Listen + "/v1/chains/31337/")
	if err != nil {
		t.Fatal(err)
	}
	var trailingStatus apirpc.Status
	if err = trailing.Call(&trailingStatus, "invisi_getStatus"); err != nil {
		t.Fatal(err)
	}
	trailing.Close()
	if trailingStatus.Deployment.ChainID != "31337" {
		t.Fatal("trailing slash route lost chain context")
	}
	a.advanceTo.Store(2)
	advanced := waitChainStatus(t, cfg.Listen, "31337", 2, done)
	unchanged := waitChainStatus(t, cfg.Listen, "31338", 0, done)
	if advanced.IndexedHead.Number != 2 || unchanged.IndexedHead.Number != 0 {
		t.Fatalf("checkpoints not independent: %+v %+v", advanced.IndexedHead, unchanged.IndexedHead)
	}
	deadline := time.Now().Add(5 * time.Second)
	for _, chain := range cfg.Chains {
		path := filepath.Join(dir, "snapshots", chain.Deployment.ChainID, strings.ToLower(chain.Deployment.Protocol.Hex()), "latest.json")
		for {
			if _, err = os.Stat(path); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("chain %s snapshot worker did not publish", chain.Deployment.ChainID)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("multi-chain service did not cancel")
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		t.Fatalf("listener leaked after cancellation: %v", err)
	}
	listener.Close()
	for _, chain := range cfg.Chains {
		store, err := OpenDatabase(context.Background(), chain)
		if err != nil {
			t.Fatal(err)
		}
		head, err := store.Checkpoint(context.Background())
		store.Close()
		want := uint64(0)
		if chain.Deployment.ChainID == "31337" {
			want = 2
		}
		if err != nil || head == nil || head.Number != want {
			t.Fatalf("chain %s checkpoint not persisted: %+v %v", chain.Deployment.ChainID, head, err)
		}
	}
	cancelRestart, doneRestart := launchService(t, cfg, dir)
	waitChainStatus(t, cfg.Listen, "31337", 2, doneRestart)
	waitChainStatus(t, cfg.Listen, "31338", 0, doneRestart)
	cancelRestart()
	select {
	case err = <-doneRestart:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("restarted service did not cancel")
	}
}
func TestRunOneChainPreflightFailureNeverPublishesListener(t *testing.T) {
	cfg, _, missing := twoChainService(t)
	cfg.Chains[0], cfg.Chains[1] = cfg.Chains[1], cfg.Chains[0]
	cfg.Chains[1].Deployment.FromBlock = 10
	missing.latest = 15
	missing.missing = "0xa"
	err := Run(context.Background(), cfg, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "31338") || !strings.Contains(err.Error(), "required block height 10") {
		t.Fatalf("did not fail complete service preflight: %v", err)
	}
	listener, e := net.Listen("tcp", cfg.Listen)
	if e != nil {
		t.Fatalf("failed startup published or leaked listener: %v", e)
	}
	listener.Close()
	for i, chain := range cfg.Chains {
		store, e := OpenDatabase(context.Background(), chain)
		if e != nil {
			t.Fatal(e)
		}
		head, e := store.Checkpoint(context.Background())
		store.Close()
		if e != nil {
			t.Fatal(e)
		}
		if i == 0 && (head == nil || head.Number != 0) {
			t.Fatal("first chain did not finish validated startup")
		}
		if i == 1 && head != nil {
			t.Fatal("unavailable chain published checkpoint")
		}
	}
}
func TestRunBindFailureStartsNoBackgroundWorkers(t *testing.T) {
	cfg, a, b := twoChainService(t)
	occupied, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	dir := t.TempDir()
	if err = Run(context.Background(), cfg, dir); err == nil {
		t.Fatal("service ignored HTTP bind error")
	}
	aCalls, bCalls := a.logsCalls.Load(), b.logsCalls.Load()
	time.Sleep(50 * time.Millisecond)
	if a.logsCalls.Load() != aCalls || b.logsCalls.Load() != bCalls {
		t.Fatal("index worker ran after bind failure")
	}
	if _, err = os.Stat(filepath.Join(dir, "snapshots")); !os.IsNotExist(err) {
		t.Fatal("snapshot worker ran despite bind failure")
	}
	for _, chain := range cfg.Chains {
		store, err := OpenDatabase(context.Background(), chain)
		if err != nil {
			t.Fatal(err)
		}
		store.Close()
	}
}

func TestRunCancellationDuringPreflightReleasesResources(t *testing.T) {
	cfg, _, blocked := twoChainService(t)
	blocked.awaitLatest = make(chan struct{}, 1)
	cancel, done := launchService(t, cfg, t.TempDir())
	select {
	case <-blocked.awaitLatest:
	case <-time.After(5 * time.Second):
		t.Fatal("preflight did not reach blocked upstream")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("canceled startup failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not interrupt upstream preflight")
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		t.Fatalf("canceled startup leaked listener: %v", err)
	}
	listener.Close()
	store, err := OpenDatabase(context.Background(), cfg.Chains[0])
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	head, err := store.Checkpoint(context.Background())
	if err != nil || head != nil {
		t.Fatalf("canceled startup published checkpoint %+v: %v", head, err)
	}
}

func TestRunVersionDiscoveryAndRPCAreCached(t *testing.T) {
	cfg, a, b := twoChainService(t)
	cancel, done := launchService(t, cfg, t.TempDir())
	defer cancel()
	waitChainStatus(t, cfg.Listen, "31337", 0, done)
	waitChainStatus(t, cfg.Listen, "31338", 0, done)
	client := &http.Client{Timeout: time.Second}
	for _, path := range []string{"/v1", "/v1/"} {
		response, err := client.Get("http://" + cfg.Listen + path)
		if err != nil {
			t.Fatal(err)
		}
		var discovery struct {
			APIVersion string           `json:"apiVersion"`
			Chains     []discoveryChain `json:"chains"`
		}
		err = json.NewDecoder(response.Body).Decode(&discovery)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK || discovery.APIVersion != "v1" || len(discovery.Chains) != 2 {
			t.Fatalf("invalid version discovery at %s: %+v", path, discovery)
		}
		for i, chain := range discovery.Chains {
			if chain.ChainID != cfg.Chains[i].Deployment.ChainID || chain.ProtocolVersion != "1" || chain.RPCPath != "/v1/chains/"+chain.ChainID {
				t.Fatalf("incorrect discovery version: %+v", chain)
			}
		}
	}
	for _, chain := range cfg.Chains {
		rpc, err := gethrpc.DialHTTP("http://" + cfg.Listen + "/v1/chains/" + chain.Deployment.ChainID)
		if err != nil {
			t.Fatal(err)
		}
		for n := 0; n < 3; n++ {
			var version apirpc.VersionInfo
			ctx, finish := context.WithTimeout(context.Background(), time.Second)
			err = rpc.CallContext(ctx, &version, "invisi_getVersion")
			finish()
			if err != nil {
				rpc.Close()
				t.Fatal(err)
			}
			if version.APIVersion != "v1" || version.ProtocolVersion != "1" {
				rpc.Close()
				t.Fatalf("incorrect cached version: %+v", version)
			}
		}
		rpc.Close()
	}
	if a.versionCalls.Load() != 1 || b.versionCalls.Load() != 1 {
		t.Fatalf("version reads were not cached independently: %d %d", a.versionCalls.Load(), b.versionCalls.Load())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("versioned service did not stop")
	}
}
func TestRunProtocolVersionFailureCleansUpBeforeHTTPPublication(t *testing.T) {
	cases := []struct {
		name     string
		result   []byte
		rpcError bool
		want     string
	}{
		{name: "malformed uint256", result: []byte{1}, want: "invalid uint256"},
		{name: "zero version", result: make([]byte, 32), want: "must be nonzero"},
		{name: "unexpected contract version", result: common.LeftPadBytes([]byte{2}, 32), want: "requires Protocol VERSION 1"},
		{name: "RPC failure", rpcError: true, want: "VERSION() RPC unavailable"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			cfg, failing, _ := twoChainService(t)
			failing.versionResult = test.result
			failing.versionError = test.rpcError
			dir := t.TempDir()
			err := Run(context.Background(), cfg, dir)
			if err == nil || !strings.Contains(err.Error(), "31337") || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Protocol version preflight unexpectedly succeeded: %v", err)
			}
			if failing.versionCalls.Load() != 1 {
				t.Fatalf("VERSION() was not queried once: %d", failing.versionCalls.Load())
			}
			listener, e := net.Listen("tcp", cfg.Listen)
			if e != nil {
				t.Fatalf("version preflight failure leaked listener: %v", e)
			}
			listener.Close()
			if _, e = os.Stat(filepath.Join(dir, "snapshots")); !os.IsNotExist(e) {
				t.Fatal("snapshot workers started before complete version preflight")
			}
			for i, chain := range cfg.Chains {
				store, e := OpenDatabase(context.Background(), chain)
				if e != nil {
					t.Fatal(e)
				}
				head, e := store.Checkpoint(context.Background())
				store.Close()
				if e != nil {
					t.Fatal(e)
				}
				if i == 0 && (head == nil || head.Number != 0) {
					t.Fatal("preceding chain did not complete startup")
				}
				if i == 1 && head != nil {
					t.Fatal("chain with invalid VERSION() published a checkpoint")
				}
			}
		})
	}
}

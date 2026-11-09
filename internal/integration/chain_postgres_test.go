// Package integration exercises the public service against an actual local EVM
// and an isolated PostgreSQL schema. No protocol SDK is used.
package integration

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	gethrpc "github.com/ethereum/go-ethereum/rpc"
	deployments "github.com/invisi-lab/invisi-protocol-rpc"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/cryptography"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/indexer"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/protocol"
	servicerpc "github.com/invisi-lab/invisi-protocol-rpc/internal/rpc"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/storage"
	"github.com/jackc/pgx/v5"
)

func TestLocalChainPostgresLifecycle(t *testing.T) {
	chainURL, databaseURL := os.Getenv("TEST_CHAIN_RPC_URL"), os.Getenv("TEST_DATABASE_URL")
	if chainURL == "" || databaseURL == "" {
		t.Skip("set TEST_CHAIN_RPC_URL and TEST_DATABASE_URL for local EVM integration")
	}
	parsed, err := url.Parse(chainURL)
	if err != nil {
		t.Fatal(err)
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || (host != "localhost" && (ip == nil || !ip.IsLoopback())) {
		t.Fatal("integration writes are restricted to a loopback EVM")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	raw, err := gethrpc.DialContext(ctx, chainURL)
	must(t, err)
	defer raw.Close()
	chain := ethclient.NewClient(raw)
	id, err := chain.ChainID(ctx)
	must(t, err)
	if id.Cmp(big.NewInt(31337)) != 0 {
		t.Fatal("integration requires chain ID 31337")
	}
	catalog, err := deployments.Load("../../testdata/deployment-fixture.json")
	must(t, err)
	local, err := catalog.Resolve("31337")
	must(t, err)
	protocolAddress := common.HexToAddress(env("TEST_PROTOCOL_ADDRESS", local.Protocol.Hex()))
	token := common.HexToAddress(env("TEST_TOKEN_ADDRESS", "0x2279B7A0a67DB372996a5FaB50D91eAA73d2eBe6"))
	pinned, err := os.ReadFile("../protocol/InvisiProtocol.abi")
	must(t, err)
	contractABI, err := abi.JSON(bytes.NewReader(pinned))
	must(t, err)
	logs, err := chain.FilterLogs(ctx, ethereum.FilterQuery{FromBlock: big.NewInt(0), Addresses: []common.Address{protocolAddress}})
	must(t, err)
	if len(logs) == 0 {
		t.Fatal("protocol has no deployment receipt logs")
	}
	code, err := chain.CodeAt(ctx, protocolAddress, nil)
	must(t, err)
	if len(code) == 0 {
		t.Fatal("protocol is not deployed")
	}
	// Trees are lazy: TreeCreated first appears on shielding, while the constructor
	// emits OwnershipTransferred. Verify the earliest protocol log is in the
	// deployment block by checking that runtime code did not exist one block earlier.
	if logs[0].BlockNumber > 0 {
		previous, e := chain.CodeAt(ctx, protocolAddress, new(big.Int).SetUint64(logs[0].BlockNumber-1))
		must(t, e)
		if len(previous) != 0 {
			t.Fatal("earliest protocol event is not its deployment receipt block")
		}
	}
	deployment := model.Deployment{ChainID: id.String(), Protocol: protocolAddress, FromBlock: logs[0].BlockNumber, RuntimeCodeHash: crypto.Keccak256Hash(code)}
	var accounts []common.Address
	must(t, raw.CallContext(ctx, &accounts, "eth_accounts"))
	if len(accounts) == 0 {
		t.Fatal("local EVM has no unlocked accounts")
	}
	var restore string
	must(t, raw.CallContext(ctx, &restore, "evm_snapshot"))
	defer func() {
		cleanup, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		var ok bool
		if e := raw.CallContext(cleanup, &ok, "evm_revert", restore); e != nil || !ok {
			t.Errorf("restore chain snapshot: %v %v", ok, e)
		}
	}()

	admin, err := pgx.Connect(ctx, databaseURL)
	must(t, err)
	defer admin.Close(context.Background())
	schema := fmt.Sprintf("invisi_integration_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted)
	must(t, err)
	defer func() {
		_, e := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		if e != nil {
			t.Errorf("drop test schema: %v", e)
		}
	}()
	dbURL, err := url.Parse(databaseURL)
	must(t, err)
	q := dbURL.Query()
	q.Set("search_path", schema)
	dbURL.RawQuery = q.Encode()
	openStore := func() *storage.Store {
		t.Helper()
		s, e := storage.Open(ctx, dbURL.String(), deployment)
		must(t, e)
		s.ValidateRoot = indexer.RootValidator(chain, protocolAddress)
		return s
	}
	store := openStore()
	defer func() {
		if store != nil {
			store.Close()
		}
	}()
	decoder, err := protocol.NewDecoder()
	must(t, err)
	ix := &indexer.Indexer{Chain: chain, Store: store, Decoder: decoder, Deployment: deployment}
	must(t, ix.Verify(ctx))
	sync := func() {
		t.Helper()
		for n := 0; n < 1000; n++ {
			worked, e := ix.Step(ctx)
			must(t, e)
			if !worked {
				return
			}
		}
		t.Fatal("indexer failed to catch up")
	}

	// The payer is unlocked only on this explicitly opted-in local development chain.
	approveABI, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"approve","inputs":[{"name":"spender","type":"address"},{"name":"amount","type":"uint256"}],"outputs":[{"type":"bool"}]}]`))
	must(t, err)
	approval, err := approveABI.Pack("approve", protocolAddress, new(big.Int).Lsh(big.NewInt(1), 200))
	must(t, err)
	send(t, ctx, raw, chain, accounts[0], token, approval)
	detectionKey := make([]byte, 32)
	detectionKey[0] = 1
	clueKey, err := cryptography.DeriveClueKey(detectionKey)
	must(t, err)
	clue, err := cryptography.CreateClueFromSeed(clueKey, make([]byte, 32))
	must(t, err)
	matched, err := cryptography.DetectClue(detectionKey, clue, 24)
	must(t, err)
	if !matched {
		t.Fatal("generated clue does not match detection key")
	}
	shield := func(seed uint64) *types.Receipt {
		t.Helper()
		header, e := chain.HeaderByNumber(ctx, nil)
		must(t, e)
		commitments := make([][32]byte, 2)
		outputs := make([]shieldOutput, 2)
		deliveries := make([]delivery, 2)
		for n := range outputs {
			identity := common.BigToHash(new(big.Int).SetUint64(seed + uint64(n)))
			amount := big.NewInt(int64(n + 1))
			inputs := make([]fr.Element, 5)
			inputs[0].SetUint64(3)
			inputs[1].SetUint64(31337)
			inputs[2].SetBytes(identity[:])
			inputs[3].SetBytes(token[:])
			inputs[4].SetBigInt(amount)
			commitment, e := cryptography.Poseidon2Hash(inputs...)
			must(t, e)
			commitments[n] = commitment.Bytes()
			outputs[n] = shieldOutput{identity, amount}
			ciphertext := make([]byte, 158)
			ciphertext[0] = 1
			ciphertext[157] = byte(seed + uint64(n))
			deliveries[n] = delivery{ciphertext, clue}
		}
		params := shieldParams{token, commitments, new(big.Int).SetUint64(header.Time + 3600)}
		funding := fundingParams{Payer: accounts[0], Amount: big.NewInt(3), SigNonce: new(big.Int), Deadline: new(big.Int), Signature: []byte{}, Permit: tokenPermit{Deadline: new(big.Int)}}
		data, e := contractABI.Pack("shield", params, outputs, deliveries, funding)
		must(t, e)
		return send(t, ctx, raw, chain, accounts[0], protocolAddress, data)
	}
	first := shield(1001)
	sync()
	firstBatch, err := store.Batch(ctx, first.BlockNumber.Uint64())
	must(t, err)
	if firstBatch == nil || len(firstBatch.Outputs) != 2 {
		t.Fatalf("expected two actual shield outputs: %+v", firstBatch)
	}
	output := firstBatch.Outputs[0]
	roots, err := store.Roots(ctx, output.TreeID, 100)
	must(t, err)
	firstRoot := roots[0]
	if firstRoot.Block.Hash != first.BlockHash {
		t.Fatalf("wrong first root anchor: %+v", firstRoot)
	}
	membership, err := store.Membership(ctx, output.TreeID, output.LeafIndex, firstRoot.Root)
	must(t, err)
	verifyMembership(t, membership)

	// HTTP JSON-RPC exercises the same handler and delegated-detection auth used by the executable.
	contractVersion, err := protocol.ReadVersion(ctx, chain, deployment.Protocol)
	must(t, err)
	if contractVersion != "1" {
		t.Fatalf("expected deployed Protocol VERSION 1, got %s", contractVersion)
	}
	api, err := servicerpc.New(store, deployment, 24, true, contractVersion)
	must(t, err)
	handler, err := servicerpc.HTTPHandler(api)
	must(t, err)
	server := httptest.NewServer(handler)
	defer server.Close()
	public, err := gethrpc.DialHTTP(server.URL)
	must(t, err)
	defer public.Close()
	detected, err := gethrpc.DialOptions(ctx, server.URL)
	must(t, err)
	defer detected.Close()
	var rpcMembership model.Membership
	must(t, public.CallContext(ctx, &rpcMembership, "invisi_getMembership", output.TreeID, output.LeafIndex, firstRoot.Root))
	verifyMembership(t, &rpcMembership)
	anchor := servicerpc.Range{FromBlock: first.BlockNumber.Uint64(), ToBlock: first.BlockNumber.Uint64(), BlockHash: first.BlockHash}
	var candidates servicerpc.DetectionResult
	must(t, detected.CallContext(ctx, &candidates, "invisi_detectNotes", anchor, hexutil.Bytes(detectionKey)))
	if len(candidates.Candidates) != 2 || candidates.CheckedBits != 24 {
		t.Fatalf("unexpected detection response: %+v", candidates)
	}
	for _, candidate := range candidates.Candidates {
		if candidate.Fallback {
			t.Fatal("valid clue unexpectedly used fallback")
		}
	}
	var rpcBlock model.Batch
	must(t, public.CallContext(ctx, &rpcBlock, "invisi_getBlock", first.BlockNumber.Uint64()))
	if len(rpcBlock.Logs) == 0 || len(rpcBlock.Outputs) != 2 {
		t.Fatal("RPC block omitted persisted raw logs or outputs")
	}
	var rpcRoots []model.Root
	must(t, public.CallContext(ctx, &rpcRoots, "invisi_getRoots", output.TreeID))
	if len(rpcRoots) < 2 {
		t.Fatal("RPC roots omitted history")
	}

	// Close every database connection and reopen without rebuilding tree state.
	server.Close()
	public.Close()
	detected.Close()
	store.Close()
	store = nil
	store = openStore()
	ix.Store = store
	membership, err = store.Membership(ctx, output.TreeID, output.LeafIndex, firstRoot.Root)
	must(t, err)
	verifyMembership(t, membership)
	var branch string
	must(t, raw.CallContext(ctx, &branch, "evm_snapshot"))
	second := shield(2001)
	sync()
	secondRoots, err := store.Roots(ctx, output.TreeID, 100)
	must(t, err)
	orphanRoot := secondRoots[0]
	membership, err = store.Membership(ctx, output.TreeID, output.LeafIndex, firstRoot.Root)
	must(t, err)
	verifyMembership(t, membership)
	if membership.Root.Block.Hash != first.BlockHash {
		t.Fatal("historical membership anchor changed after append")
	}
	var reverted bool
	must(t, raw.CallContext(ctx, &reverted, "evm_revert", branch))
	if !reverted {
		t.Fatal("branch revert failed")
	}
	alternate := shield(3001)
	if alternate.BlockHash == second.BlockHash {
		t.Fatal("alternate block hash unchanged")
	}
	sync()
	latest, err := store.Block(ctx, alternate.BlockNumber.Uint64())
	must(t, err)
	if latest == nil || latest.Hash != alternate.BlockHash {
		t.Fatal("indexer did not replace reorganized block")
	}
	if _, e := store.Membership(ctx, output.TreeID, output.LeafIndex, orphanRoot.Root); e == nil {
		t.Fatal("orphan root survived rollback")
	}
	membership, err = store.Membership(ctx, output.TreeID, output.LeafIndex, firstRoot.Root)
	must(t, err)
	verifyMembership(t, membership)
	alternateBatch, err := store.Batch(ctx, alternate.BlockNumber.Uint64())
	must(t, err)
	if alternateBatch.Block.Hash != alternate.BlockHash || len(alternateBatch.Outputs) != 2 {
		t.Fatal("alternate batch missing")
	}
	t.Logf("real shield blocks %d/%d; tree %d; first root %s; alternate block %s; restart/history/detection/reorg verified", first.BlockNumber.Uint64(), alternate.BlockNumber.Uint64(), output.TreeID, firstRoot.Root, alternate.BlockHash)
}

type shieldParams struct {
	Token             common.Address
	OutputCommitments [][32]byte
	Deadline          *big.Int
}
type shieldOutput struct {
	Identity [32]byte
	Amount   *big.Int
}
type delivery struct {
	Ciphertext []byte
	Clue       []byte
}
type tokenPermit struct {
	Deadline *big.Int
	V        uint8
	R        [32]byte
	S        [32]byte
}
type fundingParams struct {
	Payer     common.Address
	Amount    *big.Int
	SigNonce  *big.Int
	Deadline  *big.Int
	Signature []byte
	Permit    tokenPermit
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func send(t *testing.T, ctx context.Context, raw *gethrpc.Client, chain *ethclient.Client, from, to common.Address, data []byte) *types.Receipt {
	t.Helper()
	var hash common.Hash
	err := raw.CallContext(ctx, &hash, "eth_sendTransaction", map[string]any{"from": from, "to": to, "data": hexutil.Bytes(data), "gas": hexutil.Uint64(12_000_000)})
	must(t, err)
	for {
		receipt, err := chain.TransactionReceipt(ctx, hash)
		if err == nil {
			if receipt.Status != types.ReceiptStatusSuccessful {
				t.Fatalf("transaction %s reverted", hash)
			}
			return receipt
		}
		if err != ethereum.NotFound {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}
func verifyMembership(t *testing.T, m *model.Membership) {
	t.Helper()
	if len(m.Siblings) != cryptography.TreeDepth {
		t.Fatal("wrong membership depth")
	}
	current := m.Commitment
	index := m.LeafIndex
	for _, sibling := range m.Siblings {
		var err error
		if index&1 == 0 {
			current, err = cryptography.HashPair(current, sibling)
		} else {
			current, err = cryptography.HashPair(sibling, current)
		}
		must(t, err)
		index >>= 1
	}
	if current != m.Root.Root {
		t.Fatalf("membership reconstructs %s, expected %s", current, m.Root.Root)
	}
}

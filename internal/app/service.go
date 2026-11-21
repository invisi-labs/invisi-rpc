package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/invisi-labs/invisi-rpc/internal/config"
	"github.com/invisi-labs/invisi-rpc/internal/finality"
	"github.com/invisi-labs/invisi-rpc/internal/indexer"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	"github.com/invisi-labs/invisi-rpc/internal/protocol"
	apirpc "github.com/invisi-labs/invisi-rpc/internal/rpc"
	"github.com/invisi-labs/invisi-rpc/internal/snapshot"
	"github.com/invisi-labs/invisi-rpc/internal/storage"
)

// CheckAvailability fails clearly when the upstream cannot serve the next needed height.
// A checkpoint already at the current head is a normal caught-up state.
func CheckAvailability(ctx context.Context, client *ethclient.Client, store storage.Backend, c config.Chain) error {
	cp, err := store.Checkpoint(ctx)
	if err != nil {
		return err
	}
	head, err := client.HeaderByNumber(ctx, nil)
	if err != nil {
		return fmt.Errorf("Chain RPC cannot provide latest height: %w", err)
	}
	required := c.Deployment.IndexFromBlock()
	if cp != nil {
		if cp.Number > head.Number.Uint64() {
			return fmt.Errorf("Chain RPC latest height %d is behind saved checkpoint height %d", head.Number.Uint64(), cp.Number)
		}
		if cp.Number == head.Number.Uint64() {
			return nil
		}
		if _, err = client.HeaderByNumber(ctx, new(big.Int).SetUint64(cp.Number)); err != nil {
			return fmt.Errorf("Chain RPC cannot serve saved checkpoint height %d: %w", cp.Number, err)
		}
		required = cp.Number + 1
	}
	if required > head.Number.Uint64() {
		return fmt.Errorf("Chain RPC latest height %d precedes required starting height %d", head.Number.Uint64(), required)
	}
	header, err := client.HeaderByNumber(ctx, new(big.Int).SetUint64(required))
	if err != nil {
		return fmt.Errorf("Chain RPC cannot serve required block height %d: %w", required, err)
	}
	hash := header.Hash()
	if _, err = client.FilterLogs(ctx, ethereum.FilterQuery{BlockHash: &hash, Addresses: []common.Address{c.Deployment.Protocol}}); err != nil {
		return fmt.Errorf("Chain RPC cannot serve logs at required height %d: %w", required, err)
	}
	// Root validation requires state at each indexed height, not merely header retention.
	contracts := []model.ContractDeployment{{Address: c.Deployment.Protocol, RuntimeCodeHash: c.Deployment.RuntimeCodeHash, FromBlock: c.Deployment.FromBlock}}
	if c.Deployment.BroadcasterRegistry != nil {
		contracts = append(contracts, *c.Deployment.BroadcasterRegistry)
	}
	if c.Deployment.Registry != nil {
		contracts = append(contracts, *c.Deployment.Registry)
	}
	for _, contract := range contracts {
		height := required
		if height < contract.FromBlock {
			height = contract.FromBlock
		}
		code, err := client.CodeAt(ctx, contract.Address, new(big.Int).SetUint64(height))
		if err != nil {
			return fmt.Errorf("Chain RPC cannot serve historical state at required height %d: %w", height, err)
		}
		if len(code) == 0 || crypto.Keccak256Hash(code) != contract.RuntimeCodeHash {
			return fmt.Errorf("contract runtime code mismatch at required block height %d", height)
		}
	}
	return nil
}

type chainRuntime struct {
	config          config.Chain
	client          *ethclient.Client
	store           storage.Backend
	indexer         *indexer.Indexer
	handler         http.Handler
	protocolVersion string
}

// Run initializes selected deployments, then applies chain configuration changes live.
func Run(ctx context.Context, c config.Config, dir string) error {
	return runWithInterval(ctx, c, dir, time.Second)
}

func initializeChain(ctx context.Context, c config.Chain) (*chainRuntime, error) {
	return prepareChain(ctx, c, true)
}

// A replacement must not run an indexing step concurrently with the active worker.
func prepareChain(ctx context.Context, c config.Chain, reconcile bool) (runtime *chainRuntime, err error) {
	client, err := ethclient.DialContext(ctx, c.ChainRPC)
	if err != nil {
		return nil, err
	}
	var store storage.Backend
	defer func() {
		if err != nil {
			if store != nil {
				store.Close()
			}
			client.Close()
		}
	}()
	store, err = OpenDatabase(ctx, c)
	if err != nil {
		return nil, err
	}
	decoder, err := protocol.NewDeploymentDecoder(c.Deployment)
	if err != nil {
		return nil, err
	}
	finalityReader := &finality.Reader{RPC: client.Client(), Store: store}
	ix := &indexer.Indexer{BatchSize: c.IndexBatchSize, Chain: indexer.NewRPCChain(client), Store: store, Decoder: decoder, Deployment: c.Deployment, Finality: finalityReader}
	checkCtx, done := context.WithTimeout(ctx, 30*time.Second)
	err = ix.Verify(checkCtx)
	finalityErr := finalityReader.Check(checkCtx)
	blockedByFinality := finalityErr != nil
	if blockedByFinality {
		slog.Error("native finality verification failed; indexing paused", "chainId", c.Deployment.ChainID, "error", finalityErr)
	}
	if err == nil && !blockedByFinality {
		err = CheckAvailability(checkCtx, client, store, c)
	}
	var contractVersion string
	if err == nil {
		contractVersion, err = protocol.ReadVersion(checkCtx, client, c.Deployment.Protocol)
	}
	if err == nil && contractVersion != protocol.SupportedVersion {
		err = fmt.Errorf("%s requires Protocol VERSION %s; got %s", protocol.APIVersion, protocol.SupportedVersion, contractVersion)
	}
	done()
	if err != nil {
		return nil, err
	}
	store.SetRootValidator(indexer.RootValidator(client, c.Deployment.Protocol))
	// A finality fence preserves the saved read-only index instead of rewriting disputed history.
	// Otherwise reconcile and validate saved state before exposing a route.
	startupCtx, startupDone := context.WithTimeout(ctx, 30*time.Second)
	if reconcile && !blockedByFinality {
		_, err = ix.Step(startupCtx)
		if errors.Is(err, finality.ErrFinalityConflict) {
			blockedByFinality = true
			err = nil
		}
	}
	if err == nil && !blockedByFinality {
		err = VerifySavedState(startupCtx, client, store, c)
	}
	startupDone()
	if err != nil {
		return nil, fmt.Errorf("validate saved index: %w", err)
	}
	api, err := apirpc.New(store, c.Deployment, contractVersion)
	if err != nil {
		return nil, err
	}
	api.Transactions = client
	api.ChainState = client
	api.HistoricalHeaders = indexer.NewRPCChain(client)
	api.Finality = finalityReader
	handler, err := apirpc.HTTPHandler(api)
	if err != nil {
		return nil, err
	}
	return &chainRuntime{config: c, client: client, store: store, indexer: ix, handler: handler, protocolVersion: contractVersion}, nil
}

type discoveryChain struct {
	ChainID         string `json:"chainId"`
	RPCPath         string `json:"rpcPath"`
	ProtocolVersion string `json:"protocolVersion"`
}

func chainRouter(runtimes []*chainRuntime) http.Handler {
	routes := make(map[string]http.Handler, len(runtimes))
	discovery := struct {
		APIVersion string           `json:"apiVersion"`
		Chains     []discoveryChain `json:"chains"`
	}{APIVersion: protocol.APIVersion, Chains: make([]discoveryChain, 0, len(runtimes))}
	for _, runtime := range runtimes {
		path := protocol.APIPrefix + "/chains/" + runtime.config.Deployment.ChainID
		routes[path] = runtime.handler
		discovery.Chains = append(discovery.Chains, discoveryChain{ChainID: runtime.config.Deployment.ChainID, RPCPath: path, ProtocolVersion: runtime.protocolVersion})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == protocol.APIPrefix || r.URL.Path == protocol.APIPrefix+"/" {
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", http.MethodGet)
				apirpc.WriteHTTPError(w, http.StatusMethodNotAllowed, "GET required")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(discovery)
			return
		}
		path := strings.TrimSuffix(r.URL.Path, "/")
		if handler, ok := routes[path]; ok {
			handler.ServeHTTP(w, r)
			return
		}
		apirpc.WriteHTTPError(w, http.StatusNotFound, "route not found")
	})
}

func (r *chainRuntime) runIndex(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(r.config.PollSeconds) * time.Second)
	defer ticker.Stop()
	waiting := false
	for {
		stepCtx, finish := context.WithTimeout(ctx, 30*time.Second)
		progress, err := r.indexer.Step(stepCtx)
		finish()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Error("indexing failed; checkpoint retained", "chainId", r.config.Deployment.ChainID, "error", err)
			waiting = false
		}
		if progress {
			waiting = false
			continue
		}
		if err == nil && !waiting {
			head, _ := r.store.Checkpoint(ctx)
			slog.Info("caught up; waiting for latest blocks", "chainId", r.config.Deployment.ChainID, "checkpoint", head)
			waiting = true
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (r *chainRuntime) runSnapshots(ctx context.Context, interval time.Duration, dir string) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var lastHash string
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			checkpoint, err := r.store.Checkpoint(ctx)
			if err != nil {
				if ctx.Err() == nil {
					slog.Error("snapshot checkpoint read failed", "chainId", r.config.Deployment.ChainID, "error", err)
				}
				continue
			}
			if checkpoint == nil || checkpoint.Hash.Hex() == lastHash {
				continue
			}
			path, err := snapshot.Export(ctx, r.store, r.config.Deployment, filepath.Join(dir, "snapshots"))
			if err != nil {
				if ctx.Err() == nil {
					slog.Error("snapshot export failed", "chainId", r.config.Deployment.ChainID, "error", err)
				}
				continue
			}
			lastHash = checkpoint.Hash.Hex()
			slog.Info("snapshot exported", "chainId", r.config.Deployment.ChainID, "path", path)
		}
	}
}

// VerifySavedState binds restored current roots to a canonical chain checkpoint.
func VerifySavedState(ctx context.Context, client *ethclient.Client, store storage.Backend, c config.Chain) error {
	cp, err := store.Checkpoint(ctx)
	if err != nil || cp == nil {
		return err
	}
	header, err := client.HeaderByNumber(ctx, new(big.Int).SetUint64(cp.Number))
	if err != nil {
		return fmt.Errorf("Chain RPC cannot serve checkpoint height %d: %w", cp.Number, err)
	}
	if header.Hash() != cp.Hash || header.Time != cp.Timestamp {
		return errors.New("saved checkpoint hash or timestamp is not canonical")
	}
	roots, err := storage.CurrentRootsAt(ctx, store, cp.Number)
	if err != nil {
		return err
	}
	validate := indexer.RootValidator(client, c.Deployment.Protocol)
	for _, root := range roots {
		root.Block = *cp
		if err = validate(ctx, root); err != nil {
			return fmt.Errorf("Chain RPC cannot validate saved tree %d at checkpoint height %d: %w", root.TreeID, cp.Number, err)
		}
	}
	return nil
}

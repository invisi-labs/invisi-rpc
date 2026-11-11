// Package rpc exposes bounded public data queries.
package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/common"
	gethrpc "github.com/ethereum/go-ethereum/rpc"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/protocol"
)

type Backend interface {
	Checkpoint(context.Context) (*model.Block, error)
	Block(context.Context, uint64) (*model.Block, error)
	Batch(context.Context, uint64) (*model.Batch, error)
	Roots(context.Context, uint64, int) ([]model.Root, error)
	Membership(context.Context, uint64, uint32, common.Hash) (*model.Membership, error)
	Note(context.Context, common.Hash, uint64) (*model.NoteLookup, error)
	Events(context.Context, uint64, uint64, int) ([]model.Event, error)
	Outputs(context.Context, uint64, uint64, int) ([]model.NoteOutput, error)
	IsSpent(context.Context, common.Hash) (bool, error)
	Broadcasters(context.Context, model.BroadcasterQuery) (*model.BroadcasterPage, error)
	ReceivingAddress(context.Context, common.Address) (*model.RegisteredReceivingAddress, error)
}
type API struct {
	Transactions TransactionReader
	Store        Backend
	Deployment   model.Deployment
	version      VersionInfo
}
type Range struct {
	FromBlock uint64      `json:"fromBlock"`
	ToBlock   uint64      `json:"toBlock"`
	BlockHash common.Hash `json:"blockHash"`
}
type VersionInfo struct {
	APIVersion      string `json:"apiVersion"`
	ProtocolVersion string `json:"protocolVersion"`
}

type Status struct {
	VersionInfo
	Deployment  model.Deployment `json:"deployment"`
	IndexedHead *model.Block     `json:"indexedHead"`
}

func New(store Backend, d model.Deployment, protocolVersion string) (*API, error) {
	if err := d.ValidateMetadata(); err != nil {
		return nil, err
	}
	if protocolVersion != protocol.SupportedVersion {
		return nil, errors.New("unsupported Protocol VERSION for " + protocol.APIVersion)
	}
	return &API{Store: store, Deployment: d, version: VersionInfo{APIVersion: protocol.APIVersion, ProtocolVersion: protocolVersion}}, nil
}
func (a *API) GetStatus(ctx context.Context) (Status, error) {
	h, e := a.Store.Checkpoint(ctx)
	return Status{VersionInfo: a.version, Deployment: a.Deployment, IndexedHead: h}, e
}

// GetDeployment is available before indexing has reached its first checkpoint.
func (a *API) GetDeployment() struct {
	VersionInfo
	Deployment model.Deployment `json:"deployment"`
} {
	return struct {
		VersionInfo
		Deployment model.Deployment `json:"deployment"`
	}{a.version, a.Deployment}
}

func (a *API) GetVersion() VersionInfo { return a.version }

func (a *API) GetBlock(ctx context.Context, number uint64) (*model.Batch, error) {
	return a.Store.Batch(ctx, number)
}
func (a *API) GetRoots(ctx context.Context, treeID uint64) ([]model.Root, error) {
	return a.Store.Roots(ctx, treeID, 10000)
}
func (a *API) GetMembership(ctx context.Context, treeID uint64, index uint32, root common.Hash) (*model.Membership, error) {
	return a.Store.Membership(ctx, treeID, index, root)
}

// GetNote returns an output and membership at or before the requested block.
// Clients must authenticate the returned root independently against the chain.
func (a *API) GetNote(ctx context.Context, commitment common.Hash, atBlock uint64) (*model.NoteLookup, error) {
	var scalar fr.Element
	if commitment == (common.Hash{}) {
		return nil, errors.New("nonzero commitment required")
	}
	if err := scalar.SetBytesCanonical(commitment[:]); err != nil {
		return nil, errors.New("canonical commitment required")
	}
	block, err := a.Store.Block(ctx, atBlock)
	if err != nil {
		return nil, err
	}
	if block == nil {
		return nil, errors.New("requested block is not indexed")
	}
	result, err := a.Store.Note(ctx, commitment, atBlock)
	if err != nil {
		return nil, err
	}
	current, err := a.Store.Block(ctx, atBlock)
	if err != nil {
		return nil, err
	}
	if current == nil || current.Hash != block.Hash {
		return nil, errors.New("note lookup anchor reorganized; retry lookup")
	}
	return result, nil
}

// MaxUpdateBlocks is the inclusive block limit for every ranged RPC query.
const MaxUpdateBlocks = 10000

// Every ranged query is bound to a canonical indexed end-block hash.
func (a *API) checkRange(ctx context.Context, r Range) error {
	if r.ToBlock < r.FromBlock || r.ToBlock-r.FromBlock >= MaxUpdateBlocks || r.BlockHash == (common.Hash{}) {
		return fmt.Errorf("range requires at most %d blocks and an indexed end blockHash", MaxUpdateBlocks)
	}
	b, e := a.Store.Block(ctx, r.ToBlock)
	if e != nil {
		return e
	}
	if b == nil || b.Hash != r.BlockHash {
		return errors.New("unknown or reorganized range anchor")
	}
	return nil
}

// Ranged responses reserve headroom below the transport's 16 MiB batch budget.
const maxRangeResponseBytes = 8 << 20

func checkResponseSize(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(encoded) > maxRangeResponseBytes {
		return errors.New("range too large; query smaller ranges or getBlock")
	}
	return nil
}

func (a *API) GetEvents(ctx context.Context, r Range) ([]model.Event, error) {
	if e := a.checkRange(ctx, r); e != nil {
		return nil, e
	}
	v, e := a.loadEvents(ctx, r)
	if e != nil {
		return nil, e
	}
	if e = a.checkRange(ctx, r); e != nil {
		return nil, e
	}
	return v, nil
}

// loadEvents retains event caps while callers own the before/after anchor checks.
func (a *API) loadEvents(ctx context.Context, r Range) ([]model.Event, error) {
	v, e := a.Store.Events(ctx, r.FromBlock, r.ToBlock, 10000)
	if e != nil {
		return nil, e
	}
	if len(v) == 10000 {
		return nil, errors.New("range too large; query smaller ranges or getBlock")
	}
	if e = checkResponseSize(v); e != nil {
		return nil, e
	}
	return v, nil
}

func (a *API) GetCreditTransitions(ctx context.Context, r Range) ([]model.Event, error) {
	events, e := a.GetEvents(ctx, r)
	if e != nil {
		return nil, e
	}
	return creditTransitions(events), nil
}

func creditTransitions(events []model.Event) []model.Event {
	out := []model.Event{}
	for _, v := range events {
		switch v.Name {
		case "CreditCreated", "CreditDraw", "CreditUpdated", "CreditRevoked":
			out = append(out, v)
		}
	}
	return out
}

func HTTPHandler(a *API) (http.Handler, error) {
	s := gethrpc.NewServer()
	s.SetBatchLimits(20, 16<<20)
	s.SetHTTPBodyLimit(1 << 20)
	if e := s.RegisterName("invisi", a); e != nil {
		return nil, e
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		s.ServeHTTP(w, r.WithContext(ctx))
	}), nil
}

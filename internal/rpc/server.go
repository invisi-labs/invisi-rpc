// Package rpc exposes bounded public data queries and optional delegated detection.
package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	gethrpc "github.com/ethereum/go-ethereum/rpc"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/cryptography"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/protocol"
)

type Backend interface {
	Checkpoint(context.Context) (*model.Block, error)
	Block(context.Context, uint64) (*model.Block, error)
	Batch(context.Context, uint64) (*model.Batch, error)
	Roots(context.Context, uint64, int) ([]model.Root, error)
	Membership(context.Context, uint64, uint32, common.Hash) (*model.Membership, error)
	Events(context.Context, uint64, uint64, int) ([]model.Event, error)
	Outputs(context.Context, uint64, uint64, int) ([]model.Output, error)
	IsSpent(context.Context, common.Hash) (bool, error)
	Broadcasters(context.Context, model.BroadcasterQuery) (*model.BroadcasterPage, error)
	ReceivingAddress(context.Context, common.Address) (*model.RegisteredReceivingAddress, error)
}
type API struct {
	Transactions     TransactionReader
	Store            Backend
	Deployment       model.Deployment
	version          VersionInfo
	DetectionBits    int
	detectionEnabled bool
	detectionSlots   chan struct{}
	mu               sync.Mutex
	prepared         map[string]*cryptography.PreparedClue
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
	Deployment       model.Deployment `json:"deployment"`
	IndexedHead      *model.Block     `json:"indexedHead"`
	DetectionEnabled bool             `json:"detectionEnabled"`
	CheckedBits      int              `json:"checkedBits"`
}

func New(store Backend, d model.Deployment, bits int, enabled bool, protocolVersion string) (*API, error) {
	if err := d.ValidateMetadata(); err != nil {
		return nil, err
	}
	if protocolVersion != protocol.SupportedVersion {
		return nil, errors.New("unsupported Protocol VERSION for " + protocol.APIVersion)
	}
	if bits < 1 || bits > 24 {
		return nil, errors.New("detection bits must be 1..24")
	}
	return &API{Store: store, Deployment: d, DetectionBits: bits, version: VersionInfo{APIVersion: protocol.APIVersion, ProtocolVersion: protocolVersion}, detectionEnabled: enabled, detectionSlots: make(chan struct{}, 2), prepared: make(map[string]*cryptography.PreparedClue)}, nil
}
func (a *API) GetStatus(ctx context.Context) (Status, error) {
	h, e := a.Store.Checkpoint(ctx)
	return Status{VersionInfo: a.version, Deployment: a.Deployment, IndexedHead: h, DetectionEnabled: a.detectionEnabled, CheckedBits: a.DetectionBits}, e
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
func (a *API) IsSpent(ctx context.Context, nullifier common.Hash) (bool, error) {
	return a.Store.IsSpent(ctx, nullifier)
}

// Every ranged query is bound to a canonical indexed end-block hash.
func (a *API) checkRange(ctx context.Context, r Range) error {
	if r.ToBlock < r.FromBlock || r.ToBlock-r.FromBlock > 99 || r.BlockHash == (common.Hash{}) {
		return errors.New("range requires at most 100 blocks and an indexed end blockHash")
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
	if e = a.checkRange(ctx, r); e != nil {
		return nil, e
	}
	return v, nil
}

type Updates struct {
	BlockHash         common.Hash    `json:"blockHash"`
	Outputs           []model.Output `json:"outputs"`
	CreditTransitions []model.Event  `json:"creditTransitions"`
}

func (a *API) GetUpdates(ctx context.Context, r Range) (*Updates, error) {
	if e := a.checkRange(ctx, r); e != nil {
		return nil, e
	}
	v, e := a.Store.Outputs(ctx, r.FromBlock, r.ToBlock, 10000)
	if e != nil {
		return nil, e
	}
	if len(v) == 10000 {
		return nil, errors.New("range too large; query smaller ranges or getBlock")
	}
	events, e := a.GetCreditTransitions(ctx, r)
	if e != nil {
		return nil, e
	}
	if e = a.checkRange(ctx, r); e != nil {
		return nil, e
	}
	result := &Updates{r.BlockHash, v, events}
	if e = checkResponseSize(result); e != nil {
		return nil, e
	}
	return result, nil
}
func (a *API) GetCreditTransitions(ctx context.Context, r Range) ([]model.Event, error) {
	events, e := a.GetEvents(ctx, r)
	if e != nil {
		return nil, e
	}
	out := []model.Event{}
	for _, v := range events {
		switch v.Name {
		case "CreditCreated", "CreditDraw", "CreditCancelled", "CreditToppedUp":
			out = append(out, v)
		}
	}
	return out, nil
}

type Candidate struct {
	Output   model.Output `json:"output"`
	Fallback bool         `json:"fallback"`
}
type DetectionResult struct {
	BlockHash         common.Hash   `json:"blockHash"`
	CheckedBits       int           `json:"checkedBits"`
	Candidates        []Candidate   `json:"candidates"`
	CreditTransitions []model.Event `json:"creditTransitions"`
}

func (a *API) DetectOutputs(ctx context.Context, r Range, key hexutil.Bytes) (*DetectionResult, error) {
	if !a.detectionEnabled {
		return nil, errors.New("delegated detection is disabled")
	}
	select {
	case a.detectionSlots <- struct{}{}:
		defer func() { <-a.detectionSlots }()
	default:
		return nil, capacityError{}
	}
	if err := cryptography.ValidateDetectionKey(key); err != nil {
		return nil, err
	}
	updates, e := a.GetUpdates(ctx, r)
	if e != nil {
		return nil, e
	}
	if len(updates.Outputs) > 1000 {
		return nil, errors.New("detection range exceeds 1000 deliveries; use a smaller range")
	}
	result := &DetectionResult{BlockHash: r.BlockHash, CheckedBits: a.DetectionBits, Candidates: []Candidate{}, CreditTransitions: updates.CreditTransitions}
	for _, v := range updates.Outputs {
		if e = ctx.Err(); e != nil {
			return nil, e
		}
		a.mu.Lock()
		p := a.prepared[string(v.Clue)]
		a.mu.Unlock()
		if p == nil {
			p, e = cryptography.PrepareClue(v.Clue)
			if e != nil || !p.Valid() {
				result.Candidates = append(result.Candidates, Candidate{v, true})
				continue
			}
			a.mu.Lock()
			if len(a.prepared) >= 1024 {
				a.prepared = make(map[string]*cryptography.PreparedClue)
			}
			a.prepared[string(v.Clue)] = p
			a.mu.Unlock()
		}
		matched, err := cryptography.DetectPreparedClue(key, p, a.DetectionBits)
		if err != nil {
			return nil, err
		}
		if matched {
			result.Candidates = append(result.Candidates, Candidate{v, false})
		}
	}
	if e = a.checkRange(ctx, r); e != nil {
		return nil, e
	}
	return result, nil
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

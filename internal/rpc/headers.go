package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/big"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	"github.com/invisi-labs/invisi-rpc/internal/storage"
)

type HeaderRangeRequest struct {
	FromBlock uint64 `json:"fromBlock"`
	ToBlock   uint64 `json:"toBlock"`
}

func (r *HeaderRangeRequest) UnmarshalJSON(raw []byte) error {
	var fields struct {
		From *uint64 `json:"fromBlock"`
		To   *uint64 `json:"toBlock"`
	}
	if e := json.Unmarshal(raw, &fields); e != nil {
		return e
	}
	if fields.From == nil || fields.To == nil {
		return errors.New("both historical range heights are required")
	}
	r.FromBlock = *fields.From
	r.ToBlock = *fields.To
	return nil
}

type HeaderRangeResponse struct {
	VersionInfo
	Deployment model.Deployment    `json:"deployment"`
	Range      Range               `json:"range"`
	Headers    []model.BlockHeader `json:"headers"`
}
type HistoricalHeaderSource interface {
	Headers(context.Context, uint64, uint64) ([]*types.Header, error)
}
type historicalHeaderStore interface {
	Headers(context.Context, uint64, uint64) ([]model.BlockHeader, error)
	CacheHeaders(context.Context, []*types.Header, func(context.Context) error) error
}

// Only absent historical material permits compatibility fallback. All integrity,
// transport, finality and request errors remain distinct from this code.
type headersUnavailableError struct{}

func (headersUnavailableError) Error() string  { return "complete indexed headers unavailable" }
func (headersUnavailableError) ErrorCode() int { return -32004 }

// GetBlockHeaders returns indexed history, not a fresh canonical observation.
// Callers must independently recheck the live anchor before committing a scan.
func (a *API) GetBlockHeaders(ctx context.Context, r HeaderRangeRequest) (HeaderRangeResponse, error) {
	out := HeaderRangeResponse{VersionInfo: a.version, Deployment: a.Deployment}
	if r.ToBlock < r.FromBlock || r.ToBlock > math.MaxInt64 || r.ToBlock-r.FromBlock >= storage.MaxHeaderRange {
		return out, errors.New("invalid historical header range")
	}
	store, ok := a.Store.(historicalHeaderStore)
	if !ok {
		return out, headersUnavailableError{}
	}
	rows, e := store.Headers(ctx, r.FromBlock, r.ToBlock)
	if errors.Is(e, storage.ErrHeadersUnavailable) && a.HistoricalHeaders != nil && a.ChainState != nil {
		// Never fetch native history beyond the persisted index or before its start.
		cp, err := a.Store.Checkpoint(ctx)
		if err != nil {
			return out, err
		}
		if cp == nil || r.FromBlock < a.Deployment.IndexFromBlock() || r.ToBlock > cp.Number {
			return out, headersUnavailableError{}
		}
		headers := make([]*types.Header, 0, r.ToBlock-r.FromBlock+1)
		for from := r.FromBlock; from <= r.ToBlock; {
			to := min(from+127, r.ToBlock)
			part, err := a.HistoricalHeaders.Headers(ctx, from, to)
			if err != nil {
				return out, err
			}
			if len(part) != int(to-from+1) {
				return out, errors.New("incomplete upstream header range")
			}
			for i, h := range part {
				if h == nil || h.Number == nil || !h.Number.IsUint64() || h.Number.Uint64() != from+uint64(i) {
					return out, errors.New("invalid upstream header range")
				}
			}
			headers = append(headers, part...)
			from = to + 1
		}
		last := headers[len(headers)-1]
		e = store.CacheHeaders(ctx, headers, func(ctx context.Context) error {
			// This is deliberately the live native reader, not the historical cache.
			current, err := a.ChainState.HeaderByNumber(ctx, new(big.Int).SetUint64(r.ToBlock))
			if err != nil {
				return err
			}
			if current == nil || current.Number == nil || !current.Number.IsUint64() || current.Number.Uint64() != r.ToBlock || current.Hash() != last.Hash() {
				return errors.New("chain changed during header backfill")
			}
			return nil
		})
		if e == nil {
			rows, e = store.Headers(ctx, r.FromBlock, r.ToBlock)
		}
	}
	if errors.Is(e, storage.ErrHeadersUnavailable) {
		return out, headersUnavailableError{}
	}
	if e != nil {
		return out, e
	}
	out.Headers = rows
	out.Range = Range{FromBlock: r.FromBlock, ToBlock: r.ToBlock, BlockHash: rows[len(rows)-1].HeaderHash}
	if e = checkResponseSize(out); e != nil {
		return HeaderRangeResponse{}, e
	}
	return out, nil
}

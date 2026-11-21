package rpc

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-labs/invisi-rpc/internal/model"
)

// publicAPI is the HTTP registration boundary. Do not embed API or noteAPI:
// every exposed method must pass errors through publicResult. Internal callers
// retain detailed errors; clients receive no infrastructure diagnostics or data.
type publicAPI struct{ api *noteAPI }

type publicError struct{ code int }

func (e publicError) Error() string  { return "Request rejected" }
func (e publicError) ErrorCode() int { return e.code }

func publicResult(value any, err error) (any, error) {
	if err == nil {
		return value, nil
	}
	code := -32000
	var coded interface{ ErrorCode() int }
	if errors.As(err, &coded) {
		code = coded.ErrorCode()
	}
	// Deliberately do not implement ErrorData: upstream data is not proven public.
	return nil, publicError{code: code}
}

func (a *publicAPI) GetVersion() (any, error)    { return a.api.GetVersion(), nil }
func (a *publicAPI) GetDeployment() (any, error) { return a.api.GetDeployment(), nil }
func (a *publicAPI) GetStatus(ctx context.Context) (any, error) {
	return publicResult(a.api.GetStatus(ctx))
}
func (a *publicAPI) GetBlock(ctx context.Context, number uint64) (any, error) {
	return publicResult(a.api.GetBlock(ctx, number))
}
func (a *publicAPI) GetBlockHeaders(ctx context.Context, r HeaderRangeRequest) (any, error) {
	value, err := a.api.GetBlockHeaders(ctx, r)
	if err != nil {
		var unavailable headersUnavailableError
		if !errors.As(err, &unavailable) {
			err = errors.New("historical header request failed")
		}
	}
	return publicResult(value, err)
}
func (a *publicAPI) GetRoots(ctx context.Context, treeID uint64) (any, error) {
	return publicResult(a.api.GetRoots(ctx, treeID))
}
func (a *publicAPI) GetMembership(ctx context.Context, treeID uint64, index uint32, root common.Hash) (any, error) {
	return publicResult(a.api.GetMembership(ctx, treeID, index, root))
}
func (a *publicAPI) GetNote(ctx context.Context, commitment common.Hash, selector json.RawMessage) (any, error) {
	return publicResult(a.api.GetNote(ctx, commitment, selector))
}
func (a *publicAPI) GetEvents(ctx context.Context, r Range) (any, error) {
	return publicResult(a.api.GetEvents(ctx, r))
}
func (a *publicAPI) GetCreditTransitions(ctx context.Context, r Range) (any, error) {
	return publicResult(a.api.GetCreditTransitions(ctx, r))
}
func (a *publicAPI) GetNoteUpdates(ctx context.Context, r Range) (any, error) {
	return publicResult(a.api.GetNoteUpdates(ctx, r))
}
func (a *publicAPI) GetCreditState(ctx context.Context, c common.Hash, n uint64) (any, error) {
	return publicResult(a.api.GetCreditState(ctx, c, n))
}
func (a *publicAPI) IsNoteSpent(ctx context.Context, h common.Hash, n uint64) (any, error) {
	return publicResult(a.api.IsNoteSpent(ctx, h, n))
}
func (a *publicAPI) IsCreditConsumed(ctx context.Context, h common.Hash, n uint64) (any, error) {
	return publicResult(a.api.IsCreditConsumed(ctx, h, n))
}
func (a *publicAPI) GetCreditStatus(ctx context.Context, owner, token common.Address, grantID string, n uint64) (any, error) {
	return publicResult(a.api.GetCreditStatus(ctx, owner, token, grantID, n))
}

func (a *publicAPI) GetTransactionNoteUpdates(ctx context.Context, h common.Hash) (any, error) {
	return publicResult(a.api.GetTransactionNoteUpdates(ctx, h))
}
func (a *publicAPI) GetTransactionCreditUpdates(ctx context.Context, h common.Hash) (any, error) {
	return publicResult(a.api.GetTransactionCreditUpdates(ctx, h))
}
func (a *publicAPI) GetReceivingAddress(ctx context.Context, owner common.Address) (any, error) {
	return publicResult(a.api.GetReceivingAddress(ctx, owner))
}
func (a *publicAPI) GetBroadcasters(ctx context.Context, q model.BroadcasterQuery) (any, error) {
	return publicResult(a.api.GetBroadcasters(ctx, q))
}
func (a *publicAPI) GetSettlementOperations(ctx context.Context, q OperationsQuery) (any, error) {
	return publicResult(a.api.GetSettlementOperations(ctx, q))
}
func (a *publicAPI) GetProtocolTransactions(ctx context.Context, q OperationsQuery) (any, error) {
	return publicResult(a.api.GetProtocolTransactions(ctx, q))
}
func (a *publicAPI) GetTransactionOperations(ctx context.Context, h common.Hash) (any, error) {
	return publicResult(a.api.GetTransactionOperations(ctx, h))
}
func (a *publicAPI) ReadChainState(ctx context.Context, r ChainStateRequest) (any, error) {
	return publicResult(a.api.ReadChainState(ctx, r))
}

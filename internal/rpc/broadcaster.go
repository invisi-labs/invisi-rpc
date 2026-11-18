package rpc

import (
	"context"
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-labs/invisi-rpc/internal/model"
)

// GetBroadcasters returns registry declarations, not an endorsement or live health check.
func (a *API) GetBroadcasters(ctx context.Context, q model.BroadcasterQuery) (*model.BroadcasterPage, error) {
	if a.Deployment.BroadcasterRegistry == nil {
		return nil, errors.New("deployment has no broadcaster registry")
	}
	if q.Limit < 0 || q.Limit > 100 {
		return nil, errors.New("limit must be 1..100")
	}
	if q.After != (common.Address{}) && q.Block == nil {
		return nil, errors.New("pagination requires indexed block anchor")
	}
	if q.Limit == 0 {
		q.Limit = 100
	}
	page, err := a.Store.Broadcasters(ctx, q)
	if err != nil {
		return nil, err
	}
	if err = checkResponseSize(page); err != nil {
		return nil, err
	}
	return page, nil
}

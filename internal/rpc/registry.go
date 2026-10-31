package rpc

import (
	"context"
	"errors"
	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
)

// GetReceivingAddress returns registered keys through the indexed head.
func (a *API) GetReceivingAddress(ctx context.Context, owner common.Address) (*model.RegisteredReceivingAddress, error) {
	if owner == (common.Address{}) {
		return nil, errors.New("wallet identity must be a nonzero EVM address")
	}
	if a.Deployment.Registry == nil {
		return nil, errors.New("deployment has no receiving-key registry")
	}
	return a.Store.ReceivingAddress(ctx, owner)
}

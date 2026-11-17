package rpc

import (
	"context"
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/invisi-lab/invisi-rpc/internal/model"
)

// RegisteredReceivingAddress is the public lookup result. Registry event and
// persisted registration names remain independent of this public API.
type RegisteredReceivingAddress struct {
	OwnerAddress     common.Address `json:"ownerAddress"`
	ViewingPublicKey hexutil.Bytes  `json:"viewingPublicKey"`
	ClueKey          common.Hash    `json:"clueKey"`
	Nonce            uint64         `json:"nonce,string"`
	Block            model.Block    `json:"block"`
}

// GetReceivingAddress returns registered keys through the indexed head.
func (a *API) GetReceivingAddress(ctx context.Context, owner common.Address) (*RegisteredReceivingAddress, error) {
	if owner == (common.Address{}) {
		return nil, errors.New("owner address must be a nonzero EVM address")
	}
	if a.Deployment.Registry == nil {
		return nil, errors.New("deployment has no receiving-key registry")
	}
	registration, err := a.Store.ReceivingAddress(ctx, owner)
	if err != nil || registration == nil {
		return nil, err
	}
	return &RegisteredReceivingAddress{
		OwnerAddress:     registration.WalletIdentity,
		ViewingPublicKey: registration.ViewingPublicKey,
		ClueKey:          registration.ClueKey,
		Nonce:            registration.Nonce,
		Block:            registration.Block,
	}, nil
}

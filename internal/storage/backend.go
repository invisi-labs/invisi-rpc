package storage

import (
	"context"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
)

// Backend is the persistence contract shared by PostgreSQL and SQLite.
// Configure its root validator before starting readers, writers, or imports.
type Backend interface {
	Close()
	SetRootValidator(func(context.Context, model.Root) error)
	Checkpoint(context.Context) (*model.Block, error)
	Block(context.Context, uint64) (*model.Block, error)
	Batch(context.Context, uint64) (*model.Batch, error)
	Apply(context.Context, model.Batch) error
	Rollback(context.Context, uint64) error
	Roots(context.Context, uint64, int) ([]model.Root, error)
	Membership(context.Context, uint64, uint32, common.Hash) (*model.Membership, error)
	Note(context.Context, common.Hash, uint64) (*model.NoteLookup, error)
	Events(context.Context, uint64, uint64, int) ([]model.Event, error)
	Outputs(context.Context, uint64, uint64, int) ([]model.Output, error)
	IsSpent(context.Context, common.Hash) (bool, error)
	Broadcasters(context.Context, model.BroadcasterQuery) (*model.BroadcasterPage, error)
	ReceivingAddress(context.Context, common.Address) (*model.RegisteredReceivingAddress, error)
	Import(context.Context, func() (model.Batch, error)) error
	Export(context.Context, func(model.Batch) error) (*model.Block, error)
}

var _ Backend = (*Store)(nil)

// OpenPostgres explicitly selects the PostgreSQL backend.
func OpenPostgres(ctx context.Context, url string, deployment model.Deployment) (*Store, error) {
	return Open(ctx, url, deployment)
}

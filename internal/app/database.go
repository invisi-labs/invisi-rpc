package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/invisi-lab/invisi-rpc/internal/config"
	"github.com/invisi-lab/invisi-rpc/internal/storage"
)

func OpenDatabase(ctx context.Context, c config.Chain) (storage.Backend, error) {
	switch c.Database.Driver {
	case "sqlite":
		if err := os.MkdirAll(filepath.Dir(c.Database.Path), 0700); err != nil {
			return nil, err
		}
		return storage.OpenSQLite(ctx, c.Database.Path, c.Deployment)
	case "postgres":
		return storage.OpenPostgres(ctx, c.Database.URL, c.Deployment)
	default:
		return nil, fmt.Errorf("unsupported database %q", c.Database.Driver)
	}
}

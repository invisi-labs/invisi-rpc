package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/url"
	"path/filepath"

	deployment "github.com/invisi-lab/invisi-protocol-rpc"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/config"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/snapshot"
	"github.com/urfave/cli/v3"
)

func rpcFlag() *cli.StringFlag {
	return &cli.StringFlag{Name: "rpc", Required: true, Sources: cli.EnvVars("CHAIN_RPC_URL"), Usage: "HTTP(S) or WS(S) upstream RPC URL"}
}
func chainID(c *cli.Command) (string, error) {
	if c.Args().Len() != 1 {
		return "", errors.New("provide exactly one canonical decimal chain ID")
	}
	id := c.Args().First()
	n, ok := new(big.Int).SetString(id, 10)
	if !ok || n.Sign() <= 0 || n.String() != id {
		return "", errors.New("provide a canonical positive decimal chain ID")
	}
	return id, nil
}
func validateRPC(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("--rpc requires a valid HTTP(S) or WS(S) URL with a host")
	}
	if (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "ws" && u.Scheme != "wss") || u.Hostname() == "" {
		return errors.New("--rpc requires a valid HTTP(S) or WS(S) URL with a host")
	}
	return nil
}
func chainCommand(output io.Writer) *cli.Command {
	return &cli.Command{Name: "chain", Usage: "Manage configured chains", Commands: []*cli.Command{
		{Name: "add", ArgsUsage: "ID", Flags: []cli.Flag{dataDirFlag(), rpcFlag(),
			&cli.StringFlag{Name: "deployment-file", Value: deployment.DefaultFile, Usage: "Protocol deployment JSON file", Sources: cli.EnvVars("INVISI_RPC_DEPLOYMENT_FILE")},
			&cli.StringFlag{Name: "database", Value: "sqlite", Sources: cli.EnvVars("INVISI_RPC_DATABASE")},
			&cli.StringFlag{Name: "database-url", Sources: cli.EnvVars("INVISI_RPC_DATABASE_URL", "DATABASE_URL")},
			&cli.StringFlag{Name: "snapshot", Sources: cli.EnvVars("INVISI_RPC_SNAPSHOT")},
			&cli.StringFlag{Name: "snapshot-sha256", Sources: cli.EnvVars("INVISI_RPC_SNAPSHOT_SHA256")},
			&cli.Int64Flag{Name: "snapshot-max-bytes", Value: snapshot.DefaultMaxDecompressedBytes, Sources: cli.EnvVars("INVISI_RPC_SNAPSHOT_MAX_BYTES")},
			&cli.StringFlag{Name: "snapshot-interval", Value: "6h", Sources: cli.EnvVars("INVISI_RPC_SNAPSHOT_INTERVAL")},
		}, Action: func(ctx context.Context, c *cli.Command) error { return addChain(ctx, c, output) }},
		{Name: "remove", ArgsUsage: "ID", Flags: []cli.Flag{dataDirFlag()}, Action: func(ctx context.Context, c *cli.Command) error { return mutateChain(ctx, c, output, false) }},
		{Name: "set-rpc", ArgsUsage: "ID", Flags: []cli.Flag{dataDirFlag(), rpcFlag()}, Action: func(ctx context.Context, c *cli.Command) error { return mutateChain(ctx, c, output, true) }},
		{Name: "list", Flags: []cli.Flag{dataDirFlag()}, Action: func(ctx context.Context, c *cli.Command) error {
			if c.Args().Len() != 0 {
				return errors.New("chain list accepts no arguments")
			}
			cfg, err := config.LoadDir(selectedDataDir(c))
			if err != nil {
				return err
			}
			type entry struct {
				ID        string `json:"id"`
				Protocol  string `json:"protocol"`
				RPCPathV1 string `json:"rpcPathV1"`
				RPCHost   string `json:"rpcHost,omitempty"`
			}
			entries := make([]entry, 0, len(cfg.Chains))
			for _, chain := range cfg.Chains {
				u, _ := url.Parse(chain.ChainRPC)
				host := ""
				if u != nil {
					host = u.Hostname()
				}
				entries = append(entries, entry{chain.Deployment.ChainID, chain.Deployment.Protocol.Hex(), "/v1/chains/" + chain.Deployment.ChainID, host})
			}
			return json.NewEncoder(output).Encode(entries)
		}},
	}}
}
func mutateChain(ctx context.Context, c *cli.Command, output io.Writer, setRPC bool) error {
	id, err := chainID(c)
	if err != nil {
		return err
	}
	if setRPC {
		if err = validateRPC(c.String("rpc")); err != nil {
			return err
		}
	}
	err = config.Update(ctx, selectedDataDir(c), func(cfg *config.Config) error {
		for i := range cfg.Chains {
			if cfg.Chains[i].Deployment.ChainID == id {
				if setRPC {
					cfg.Chains[i].ChainRPC = c.String("rpc")
				} else {
					cfg.Chains = append(cfg.Chains[:i], cfg.Chains[i+1:]...)
				}
				return nil
			}
		}
		return fmt.Errorf("chain %s is not configured", id)
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Saved chain %s configuration; a running service will reload it automatically.\n", id)
	return err
}
func addChain(ctx context.Context, c *cli.Command, output io.Writer) error {
	id, err := chainID(c)
	if err != nil {
		return err
	}
	if err = validateRPC(c.String("rpc")); err != nil {
		return err
	}
	limit := c.Int64("snapshot-max-bytes")
	if limit <= 0 || limit == math.MaxInt64 {
		return errors.New("--snapshot-max-bytes must be positive and below MaxInt64")
	}
	source, digest := c.String("snapshot"), c.String("snapshot-sha256")
	if digest != "" && source == "" {
		return errors.New("--snapshot-sha256 requires --snapshot")
	}
	driver := c.String("database")
	if driver != "postgres" && c.IsSet("database-url") {
		return errors.New("--database-url requires --database postgres")
	}
	catalog, err := deployment.Load(c.String("deployment-file"))
	if err != nil {
		return fmt.Errorf("load deployment catalog: %w", err)
	}
	dep, err := catalog.Resolve(id)
	if err != nil {
		return err
	}
	chain := config.Chain{ChainRPC: c.String("rpc"), Deployment: dep, SnapshotInterval: c.String("snapshot-interval"), Database: config.Database{Driver: driver}}
	if driver == "sqlite" {
		chain.Database.Path = filepath.Join("chains", id, "protocol.sqlite")
	} else {
		chain.Database.URL = c.String("database-url")
	}
	if source != "" {
		h, e := snapshot.InspectContext(ctx, source, limit)
		if e != nil {
			return fmt.Errorf("inspect snapshot for chain %s: %w", id, e)
		}
		if h.Deployment != dep {
			return fmt.Errorf("snapshot deployment for chain %s differs from deployment catalog", id)
		}
		digest, err = snapshotDigest(ctx, source, digest)
		if err != nil {
			return err
		}
	}
	reused := false
	err = config.Update(ctx, selectedDataDir(c), func(cfg *config.Config) error {
		for _, existing := range cfg.Chains {
			if existing.Deployment.ChainID == id {
				return fmt.Errorf("duplicate chain %s", id)
			}
		}
		candidate := *cfg
		candidate.Chains = append(append([]config.Chain{}, cfg.Chains...), chain)
		if e := candidate.Validate(); e != nil {
			return e
		}
		opened := chain
		if driver == "sqlite" {
			dir, e := filepath.Abs(selectedDataDir(c))
			if e != nil {
				return e
			}
			opened.Database.Path = filepath.Join(dir, chain.Database.Path)
		}
		store, e := OpenDatabase(ctx, opened)
		if e != nil {
			return e
		}
		defer store.Close()
		if source != "" {
			head, e := store.Checkpoint(ctx)
			if e != nil {
				return e
			}
			if head != nil {
				matches, e := snapshot.Matches(ctx, store, dep, source, digest, limit)
				if e != nil {
					return e
				}
				if !matches {
					return fmt.Errorf("snapshot target for chain %s is not empty and differs from requested snapshot", id)
				}
				reused = true
			} else if e = snapshot.RestoreWithLimit(ctx, store, dep, source, digest, limit); e != nil {
				return fmt.Errorf("restore snapshot for chain %s: %w", id, e)
			}
		}
		cfg.Chains = candidate.Chains
		return nil
	})
	if err != nil {
		return err
	}
	if reused {
		if _, err = fmt.Fprintf(output, "Chain %s already restored; reusing existing snapshot data.\n", id); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(output, "Saved chain %s configuration; a running service will reload and validate it automatically.\n", id)
	return err
}

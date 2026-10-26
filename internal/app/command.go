package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/invisi-lab/invisi-protocol-rpc/internal/config"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/snapshot"
	"github.com/urfave/cli/v3"
)

const DefaultDataDir = "./invisi-protocol-rpc"

func dataDirFlag() *cli.StringFlag {
	return &cli.StringFlag{Name: "dir", Value: DefaultDataDir, Sources: cli.EnvVars("INVISI_RPC_DATA_DIR"), Usage: "Service data directory"}
}

func selectedDataDir(c *cli.Command) string {
	if value := c.String("dir"); value != "" {
		return value
	}
	return DefaultDataDir
}

func Command(output io.Writer) *cli.Command {
	return &cli.Command{Name: "invisi-protocol-rpc", Usage: "Index public Invisi protocol data for selected chains", Writer: output, SliceFlagSeparator: "\n", Commands: []*cli.Command{
		{Name: "init", Usage: "Initialize an empty service directory", ArgsUsage: "[DATA_DIR]", Flags: []cli.Flag{dataDirFlag(), &cli.StringFlag{Name: "listen", Sources: cli.EnvVars("INVISI_RPC_LISTEN"), Value: "127.0.0.1:8080"}}, Action: func(ctx context.Context, c *cli.Command) error { return initialize(ctx, c, output) }},
		chainCommand(output),
		{Name: "run", Usage: "Resume all configured chains on one listener", Flags: []cli.Flag{dataDirFlag(), &cli.StringFlag{Name: "listen", Sources: cli.EnvVars("INVISI_RPC_LISTEN"), Usage: "Override the saved listen address"}}, Action: func(ctx context.Context, c *cli.Command) error {
			dir, err := filepath.Abs(selectedDataDir(c))
			if err != nil {
				return err
			}
			cfg, err := config.LoadDir(dir)
			if err != nil {
				return fmt.Errorf("load initialized directory (run init first): %w", err)
			}
			if c.IsSet("listen") {
				cfg.Listen = c.String("listen")
			}
			return Run(ctx, cfg, dir)
		}},
		{Name: "snapshot", Usage: "Manage portable per-chain index snapshots", Commands: []*cli.Command{
			{Name: "export", Usage: "Export an immutable snapshot and latest manifest", Flags: []cli.Flag{dataDirFlag(), &cli.StringFlag{Name: "chain", Sources: cli.EnvVars("INVISI_RPC_CHAIN"), Usage: "Selected chain ID (required for a multi-chain directory)"}, &cli.StringFlag{Name: "output", Sources: cli.EnvVars("INVISI_RPC_SNAPSHOT_OUTPUT"), Usage: "Snapshot base directory (default DATA_DIR/snapshots)"}}, Action: func(ctx context.Context, c *cli.Command) error {
				dir, err := filepath.Abs(selectedDataDir(c))
				if err != nil {
					return err
				}
				cfg, err := config.LoadDir(dir)
				if err != nil {
					return err
				}
				if len(cfg.Chains) == 0 {
					return errors.New("snapshot export requires a configured chain; use chain add first")
				}
				selected := c.String("chain")
				if selected == "" {
					if len(cfg.Chains) != 1 {
						return errors.New("snapshot export requires --chain for a multi-chain directory")
					}
					selected = cfg.Chains[0].Deployment.ChainID
				}
				var chain *config.Chain
				for n := range cfg.Chains {
					if cfg.Chains[n].Deployment.ChainID == selected {
						chain = &cfg.Chains[n]
						break
					}
				}
				if chain == nil {
					return fmt.Errorf("chain %s is not configured", selected)
				}
				store, err := OpenDatabase(ctx, *chain)
				if err != nil {
					return err
				}
				defer store.Close()
				base := c.String("output")
				if base == "" {
					base = filepath.Join(dir, "snapshots")
				}
				path, err := snapshot.Export(ctx, store, chain.Deployment, base)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(output, path)
				return err
			}},
			{Name: "inspect", Usage: "Validate a snapshot and display its deployment and checkpoint", ArgsUsage: "SNAPSHOT_FILE", Flags: []cli.Flag{&cli.Int64Flag{Name: "max-bytes", Sources: cli.EnvVars("INVISI_RPC_SNAPSHOT_MAX_BYTES"), Value: snapshot.DefaultMaxDecompressedBytes}}, Action: func(ctx context.Context, c *cli.Command) error {
				if c.Args().Len() != 1 {
					return errors.New("provide exactly one snapshot file")
				}
				h, err := snapshot.InspectContext(ctx, c.Args().First(), c.Int64("max-bytes"))
				if err != nil {
					return err
				}
				enc := json.NewEncoder(output)
				enc.SetIndent("", "  ")
				return enc.Encode(h)
			}},
		}},
	}}
}

func initialize(ctx context.Context, c *cli.Command, output io.Writer) error {
	if c.Args().Len() > 1 {
		return errors.New("init accepts at most one DATA_DIR")
	}
	selected := selectedDataDir(c)
	if c.Args().Len() == 1 {
		selected = c.Args().First()
	}
	dir, err := filepath.Abs(selected)
	if err != nil {
		return err
	}
	if info, e := os.Stat(dir); e == nil && !info.IsDir() {
		return fmt.Errorf("data directory %s is a file; select --dir or place the executable under bin/", dir)
	} else if e != nil && !os.IsNotExist(e) {
		return e
	}
	if _, err = os.Stat(config.File(dir)); err == nil {
		return errors.New("directory already initialized; configuration will not be overwritten")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = config.Save(dir, config.Config{Listen: c.String("listen"), Chains: []config.Chain{}}); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Initialized %s. Add chains with chain add ID --rpc URL; run: invisi-protocol-rpc run --dir %s\n", dir, dir)
	return err
}
func snapshotDigest(ctx context.Context, path, expected string) (string, error) {
	if expected == "" {
		raw, err := os.ReadFile(path + ".sha256")
		if err == nil {
			parts := strings.Fields(string(raw))
			if len(parts) == 0 {
				return "", errors.New("empty snapshot checksum file")
			}
			expected = parts[0]
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	if expected == "" {
		return "", nil
	}
	expected = strings.TrimPrefix(strings.TrimSpace(expected), "0x")
	want, err := hex.DecodeString(expected)
	if err != nil || len(want) != sha256.Size {
		return "", errors.New("expected SHA256 must contain 64 hexadecimal digits")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	sum := sha256.New()
	buffer := make([]byte, 128<<10)
	for {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		n, e := file.Read(buffer)
		if n > 0 {
			sum.Write(buffer[:n])
		}
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return "", e
		}
	}
	if !strings.EqualFold(expected, hex.EncodeToString(sum.Sum(nil))) {
		return "", errors.New("snapshot SHA256 mismatch")
	}
	return strings.ToLower(expected), nil
}

// Package config manages a service's initialized data directory.
package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gofrs/flock"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
)

type Database struct {
	Driver string `json:"driver"`
	Path   string `json:"path,omitempty"`
	URL    string `json:"url,omitempty"`
}
type Chain struct {
	ChainRPC         string           `json:"chainRpc"`
	Database         Database         `json:"database"`
	Deployment       model.Deployment `json:"deployment"`
	PollSeconds      int              `json:"pollSeconds"`
	SnapshotInterval string           `json:"snapshotInterval"`
}

type Config struct {
	Listen                string  `json:"listen"`
	MaxConcurrentRequests int     `json:"maxConcurrentRequests"`
	Chains                []Chain `json:"chains"`
}

func File(dir string) string             { return filepath.Join(dir, "config.json") }
func LoadDir(dir string) (Config, error) { return Load(File(dir)) }
func Load(path string) (Config, error) {
	var c Config
	absolute, err := filepath.Abs(path)
	if err != nil {
		return c, err
	}
	path = absolute
	c, err = readRaw(path)
	if err != nil {
		return c, err
	}

	for i := range c.Chains {
		chain := &c.Chains[i]
		if chain.Database.Driver == "sqlite" && !filepath.IsAbs(chain.Database.Path) && chain.Database.Path != "" {
			chain.Database.Path = filepath.Join(filepath.Dir(path), chain.Database.Path)
		}
	}
	if err = c.Validate(); err != nil {
		return c, err
	}
	return c, nil
}

func readRaw(path string) (Config, error) {
	var c Config
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return c, errors.New("configuration must contain exactly one JSON object")
	}

	return c, nil
}
func (c *Chain) Validate() error {
	if err := c.Deployment.ValidateMetadata(); err != nil {
		return err
	}
	if c.Database.Driver == "postgres" && c.Database.URL == "" {
		c.Database.URL = os.Getenv("DATABASE_URL")
	}
	if c.PollSeconds == 0 {
		c.PollSeconds = 3
	}
	if c.SnapshotInterval == "" {
		c.SnapshotInterval = "6h"
	}
	if c.PollSeconds < 1 || c.PollSeconds > 3600 {
		return errors.New("pollSeconds must be between 1 and 3600")
	}
	duration, err := time.ParseDuration(c.SnapshotInterval)
	if err != nil || duration < 0 {
		return errors.New("snapshotInterval must be a nonnegative duration (0s disables exports)")
	}
	id, ok := new(big.Int).SetString(c.Deployment.ChainID, 10)
	if !ok || id.Sign() <= 0 || id.String() != c.Deployment.ChainID {
		return errors.New("chainId must be a canonical positive decimal integer")
	}
	if c.Deployment.FromBlock > math.MaxInt64 {
		return errors.New("fromBlock exceeds database range")
	}
	if c.ChainRPC == "" || c.Deployment.Protocol == (common.Address{}) || c.Deployment.RuntimeCodeHash == (common.Hash{}) {
		return errors.New("chainRpc, protocolAddress and runtimeCodeHash are required")
	}
	switch c.Database.Driver {
	case "sqlite":
		if c.Database.Path == "" {
			return errors.New("SQLite database path is required")
		}
	case "postgres":
		if c.Database.URL == "" {
			return errors.New("PostgreSQL database URL is required (flag or DATABASE_URL)")
		}
	default:
		return fmt.Errorf("unsupported database driver %q; choose sqlite or postgres", c.Database.Driver)
	}
	return nil
}

func (c *Config) Validate() error {
	if c.MaxConcurrentRequests == 0 {
		c.MaxConcurrentRequests = 64
	}
	if c.MaxConcurrentRequests < 1 || c.MaxConcurrentRequests > 4096 {
		return errors.New("maxConcurrentRequests must be 1..4096")
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	if c.Chains == nil {
		c.Chains = []Chain{}
	}
	ids := map[string]bool{}
	paths := map[string]bool{}
	for i := range c.Chains {
		chain := &c.Chains[i]
		if err := chain.Validate(); err != nil {
			return fmt.Errorf("chain %d: %w", i, err)
		}
		if ids[chain.Deployment.ChainID] {
			return fmt.Errorf("duplicate chain ID %s", chain.Deployment.ChainID)
		}
		ids[chain.Deployment.ChainID] = true
		if chain.Database.Driver == "sqlite" {
			path, err := filepath.Abs(chain.Database.Path)
			if err != nil {
				return err
			}
			path = filepath.Clean(path)
			if resolved, err := filepath.EvalSymlinks(path); err == nil {
				path = resolved
			}
			if paths[path] {
				return fmt.Errorf("SQLite database path shared by multiple chains: %s", chain.Database.Path)
			}
			paths[path] = true
		}
	}
	return nil
}

// validateForDir preserves relative paths while validating their runtime destinations.
func validateForDir(dir string, c *Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	resolved := *c
	resolved.Chains = append([]Chain(nil), c.Chains...)
	for i := range resolved.Chains {
		db := &resolved.Chains[i].Database
		if db.Driver == "sqlite" && !filepath.IsAbs(db.Path) {
			db.Path = filepath.Join(dir, db.Path)
		}
	}
	return resolved.Validate()
}

func lockDir(ctx context.Context, dir string) (*flock.Flock, error) {
	lock := flock.New(filepath.Join(dir, ".config.lock"))
	ok, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		_ = lock.Unlock()
		return nil, err
	}
	return lock, nil
}

// publish writes and synchronizes a complete configuration before publishing it.
func publish(ctx context.Context, dir string, c Config, exclusive bool) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(raw, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if exclusive {
		// Linking publishes atomically and refuses an existing configuration.
		err = os.Link(f.Name(), File(dir))
	} else {
		err = os.Rename(f.Name(), File(dir))
	}
	if err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Save creates configuration without overwriting an initialized directory.
func Save(dir string, c Config) error {
	c.Chains = append([]Chain(nil), c.Chains...)
	if err := validateForDir(dir, &c); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock, err := lockDir(context.Background(), dir)
	if err != nil {
		return err
	}
	defer lock.Unlock()
	return publish(context.Background(), dir, c, true)
}

// Update serializes configuration changes across processes and publishes them atomically.
// The mutator receives persisted paths, including relative SQLite paths.
func Update(ctx context.Context, dir string, mutate func(*Config) error) error {
	lock, err := lockDir(ctx, dir)
	if err != nil {
		return err
	}
	defer lock.Unlock()
	c, err := readRaw(File(dir))
	if err != nil {
		return err
	}
	if err = mutate(&c); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = validateForDir(dir, &c); err != nil {
		return err
	}
	return publish(ctx, dir, c, false)
}

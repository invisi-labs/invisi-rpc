package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gofrs/flock"
	deployment "github.com/invisi-lab/invisi-protocol-rpc"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
)

func validChain(id, path string) Chain {
	return Chain{ChainRPC: "http://localhost:8545", Database: Database{Driver: "sqlite", Path: path}, Deployment: model.Deployment{ChainID: id, Protocol: common.HexToAddress("0x01"), RuntimeCodeHash: common.HexToHash("0x02"), FromBlock: 25}}
}
func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
}
func TestCatalogFilesAreIndependent(t *testing.T) {
	c, e := deployment.Load("../../deployment.json")
	if e != nil {
		t.Fatal(e)
	}
	if len(c.Chains) != 1 {
		t.Fatalf("unexpected public profiles %+v", c.Chains)
	}
	d, e := c.Resolve("31337")
	if e != nil || d.FromBlock != 26 || d.Protocol != common.HexToAddress("0xba0791095b4263a536B30E1C1FAbA2686CCb8b46") || d.RuntimeCodeHash.Hex() != "0x1f65d93baefe745834bfd8d0bd20ed740a4e09892a32b18c7058df7330e6c701" {
		t.Fatalf("file profile %+v %v", d, e)
	}
	p := filepath.Join(t.TempDir(), "private.json")
	profile := deployment.Profile{DeploymentProfile: model.DeploymentProfile{Protocol: model.ContractDeployment{Address: d.Protocol, RuntimeCodeHash: d.RuntimeCodeHash, FromBlock: 25}}}
	writeJSON(t, p, deployment.Catalog{Version: 1, Chains: map[string]deployment.Profile{"1": profile, "10": profile}})
	private, e := deployment.Load(p)
	if e != nil || len(private.Chains) != 2 {
		t.Fatalf("private catalog %+v %v", private, e)
	}
	for _, id := range []string{"1", "10"} {
		if d, e := private.Resolve(id); e != nil || d.ChainID != id {
			t.Fatalf("resolve %+v %v", d, e)
		}
	}
	if _, e = private.Resolve("31337"); e == nil {
		t.Fatal("unselected file merged into selected file")
	}
}
func TestCatalogRejectsInvalidInput(t *testing.T) {
	c, e := deployment.Load("../../deployment.json")
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(c)
	good := string(raw)
	cases := []string{
		strings.Replace(good, `"version":1`, `"version":2`, 1),
		strings.Replace(good, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(good, `"31337":`, `"031337":`, 1),
		strings.Replace(good, `"31337":`, `"0":`, 1),
		strings.Replace(good, `"31337":`, `"-1":`, 1),
		strings.Replace(good, `"name":`, `"unexpected":`, 1),
		strings.Replace(good, strings.ToLower(c.Chains["31337"].Protocol.Address.Hex()), common.Address{}.Hex(), 1),
		strings.Replace(good, c.Chains["31337"].Protocol.RuntimeCodeHash.Hex(), common.Hash{}.Hex(), 1),
		good + ` {}`, `{"version":1,"chains":{}}`,
	}
	for i, raw := range cases {
		p := filepath.Join(t.TempDir(), "catalog.json")
		if e := os.WriteFile(p, []byte(raw), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := deployment.Load(p); e == nil {
			t.Fatalf("invalid catalog %d accepted: %s", i, raw)
		}
	}
	// Duplicate chain keys must not silently replace an earlier pin.
	profile, _ := json.Marshal(c.Chains["31337"])
	duplicate := `{"version":1,"chains":{"31337":` + string(profile) + `,"31337":` + string(profile) + `}}`
	p := filepath.Join(t.TempDir(), "duplicate.json")
	os.WriteFile(p, []byte(duplicate), 0600)
	if _, e := deployment.Load(p); e == nil {
		t.Fatal("duplicate chain accepted")
	}
}
func TestMultiChainSaveLoadDefaultsAndIsolation(t *testing.T) {
	dir := t.TempDir()
	original := Config{Chains: []Chain{validChain("1", "chains/1/index.sqlite"), validChain("10", "chains/10/index.sqlite")}}
	if e := Save(dir, original); e != nil {
		t.Fatal(e)
	}
	if original.Chains[0].PollSeconds != 0 || original.Chains[0].SnapshotInterval != "" || original.Chains[0].Database.Path != "chains/1/index.sqlite" {
		t.Fatal("Save mutated caller slice")
	}
	c, e := LoadDir(dir)
	if e != nil {
		t.Fatal(e)
	}
	if c.Listen != "127.0.0.1:8080" || len(c.Chains) != 2 {
		t.Fatalf("loaded %+v", c)
	}
	for _, chain := range c.Chains {
		if !filepath.IsAbs(chain.Database.Path) || !strings.HasPrefix(chain.Database.Path, dir) || chain.PollSeconds != 3 || chain.DetectionBits != 8 || chain.SnapshotInterval != "6h" {
			t.Fatalf("defaults/path %+v", chain)
		}
	}
	info, e := os.Stat(File(dir))
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode %v %v", info, e)
	}
	if e = Save(dir, original); e == nil {
		t.Fatal("existing config overwritten")
	}
}
func TestConfigRejectsInvalidMultiChain(t *testing.T) {
	for _, kind := range []string{"duplicate-id", "shared-path", "noncanonical-id", "bad-bits", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			c := Config{Chains: []Chain{validChain("1", "one.sqlite"), validChain("10", "two.sqlite")}}
			switch kind {
			case "duplicate-id":
				c.Chains[1].Deployment.ChainID = "1"
			case "shared-path":
				c.Chains[1].Database.Path = "sub/../one.sqlite"
			case "noncanonical-id":
				c.Chains[0].Deployment.ChainID = "01"
			case "bad-bits":
				c.Chains[0].DetectionBits = 25
			case "legacy":
				p := filepath.Join(t.TempDir(), "config.json")
				writeJSON(t, p, c.Chains[0])
				if _, e := Load(p); e == nil {
					t.Fatal("legacy schema accepted")
				}
				return
			}
			if e := c.Validate(); e == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}
func TestConfigPostgresFallbackAndStrictJSON(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://from-environment")
	dir := t.TempDir()
	c := Config{Chains: []Chain{validChain("1", "unused"), validChain("10", "unused")}}
	c.Chains[0].Database = Database{Driver: "postgres"}
	c.Chains[1].Database = Database{Driver: "postgres", URL: "postgres://explicit"}
	writeJSON(t, File(dir), c)
	loaded, e := LoadDir(dir)
	if e != nil {
		t.Fatal(e)
	}
	if loaded.Chains[0].Database.URL != "postgres://from-environment" || loaded.Chains[1].Database.URL != "postgres://explicit" {
		t.Fatal("incorrect URL fallback")
	}
	raw, _ := json.Marshal(c)
	for _, bad := range []string{string(raw) + ` {}`, strings.Replace(string(raw), `"listen":`, `"unknown":`, 1)} {
		os.WriteFile(File(dir), []byte(bad), 0600)
		if _, e = LoadDir(dir); e == nil {
			t.Fatal("invalid JSON accepted")
		}
	}
}

func TestEmptyConfigAndUpdates(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, Config{}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(File(dir))
	if !bytes.Contains(raw, []byte(`"chains": []`)) {
		t.Fatalf("empty chains: %s", raw)
	}
	if err := Update(context.Background(), dir, func(c *Config) error {
		c.Chains = append(c.Chains, validChain("1", "chains/one.sqlite"))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := Update(context.Background(), dir, func(c *Config) error {
		if c.Chains[0].Database.Path != "chains/one.sqlite" {
			t.Fatal("relative path replaced")
		}
		c.Chains[0].ChainRPC = "http://replacement"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c, err := LoadDir(dir)
	if err != nil || c.Chains[0].ChainRPC != "http://replacement" || !filepath.IsAbs(c.Chains[0].Database.Path) {
		t.Fatalf("load: %+v %v", c, err)
	}
	info, _ := os.Stat(File(dir))
	if info.Mode().Perm() != 0600 {
		t.Fatal("permissions changed")
	}
	if err := Update(context.Background(), dir, func(c *Config) error { c.Chains = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	if c, err := LoadDir(dir); err != nil || len(c.Chains) != 0 {
		t.Fatalf("empty load %+v %v", c, err)
	}
}

func TestUpdateFailuresPreserveBytes(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, Config{Chains: []Chain{validChain("1", "one.sqlite")}}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(File(dir))
	for _, kind := range []string{"mutator", "invalid", "cancel", "destination-path"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := Update(ctx, dir, func(c *Config) error {
				c.Listen = "changed"
				switch kind {
				case "mutator":
					return errors.New("failure")
				case "invalid":
					c.Chains[0].DetectionBits = 25
				case "cancel":
					cancel()
				case "destination-path":
					c.Chains = append(c.Chains, validChain("10", filepath.Join(dir, "one.sqlite")))
				}
				return nil
			})
			if err == nil {
				t.Fatal("update accepted")
			}
			after, _ := os.ReadFile(File(dir))
			if !bytes.Equal(before, after) {
				t.Fatal("failed update changed bytes")
			}
		})
	}
}

func TestUpdateConcurrent(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, Config{}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 1; i <= 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- Update(context.Background(), dir, func(c *Config) error {
				c.Chains = append(c.Chains, validChain(fmt.Sprint(i), fmt.Sprintf("%d.sqlite", i)))
				return nil
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	c, err := LoadDir(dir)
	if err != nil || len(c.Chains) != 12 {
		t.Fatalf("lost updates: %d %v", len(c.Chains), err)
	}
}

func TestUpdateProcessHelper(t *testing.T) {
	dir := os.Getenv("CONFIG_UPDATE_TEST_DIR")
	if dir == "" {
		return
	}
	id := os.Getenv("CONFIG_UPDATE_TEST_ID")
	if err := Update(context.Background(), dir, func(c *Config) error {
		time.Sleep(30 * time.Millisecond)
		c.Chains = append(c.Chains, validChain(id, id+".sqlite"))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateConcurrentProcesses(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, Config{}); err != nil {
		t.Fatal(err)
	}
	commands := make([]*exec.Cmd, 4)
	for i := range commands {
		cmd := exec.Command(os.Args[0], "-test.run=^TestUpdateProcessHelper$")
		cmd.Env = append(os.Environ(), "CONFIG_UPDATE_TEST_DIR="+dir, fmt.Sprintf("CONFIG_UPDATE_TEST_ID=%d", i+1))
		commands[i] = cmd
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	c, err := LoadDir(dir)
	if err != nil || len(c.Chains) != 4 {
		t.Fatalf("lost process updates: %+v %v", c, err)
	}
}

func TestUpdateLockCancellation(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, Config{}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(File(dir))
	lock := flock.New(filepath.Join(dir, ".config.lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	called := false
	err := Update(ctx, dir, func(c *Config) error { called = true; return nil })
	if !errors.Is(err, context.DeadlineExceeded) || called {
		t.Fatalf("cancellation: %v, called %v", err, called)
	}
	after, _ := os.ReadFile(File(dir))
	if !bytes.Equal(before, after) {
		t.Fatal("canceled update changed bytes")
	}
}

func TestDeploymentMetadataRoundTrip(t *testing.T) {
	catalog, err := deployment.Load("../../deployment.json")
	if err != nil {
		t.Fatal(err)
	}
	d, err := catalog.Resolve("31337")
	if err != nil {
		t.Fatal(err)
	}
	if d.Registry == nil {
		t.Fatal("deployment metadata lost")
	}
	chain := validChain("31337", "chain.sqlite")
	chain.Deployment = d
	raw, err := json.Marshal(chain)
	if err != nil {
		t.Fatal(err)
	}
	var restored Chain
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if err := restored.Validate(); err != nil {
		t.Fatal(err)
	}
	if restored.Deployment.Registry == nil || *restored.Deployment.Registry != *d.Registry {
		t.Fatal("configuration metadata lost")
	}
	restored.Deployment.Registry.RuntimeCodeHash = common.Hash{}
	if err := restored.Validate(); err == nil {
		t.Fatal("incomplete registry metadata accepted")
	}
}

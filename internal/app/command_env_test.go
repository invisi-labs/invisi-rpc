package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	gethrpc "github.com/ethereum/go-ethereum/rpc"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/config"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
)

func TestCommandDefaultDataDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("INVISI_RPC_DATA_DIR", "")
	t.Setenv("INVISI_RPC_CHAINS", "obsolete-invalid-list")
	runCommand(t, "invisi-protocol-rpc", "init")
	cfg, e := config.LoadDir(DefaultDataDir)
	if e != nil || len(cfg.Chains) != 0 {
		t.Fatal(cfg, e)
	}
	t.Setenv("CHAIN_RPC_URL", "http://localhost:8545")
	catalog, err := os.ReadFile(privateCatalog(t, privateDeployments()))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile("deployment.json", catalog, 0600); err != nil {
		t.Fatal(err)
	}
	runCommand(t, "invisi-protocol-rpc", "chain", "add", "31337")
	cfg, e = config.LoadDir(DefaultDataDir)
	if e != nil {
		t.Fatal(e)
	}
	want, _ := filepath.Abs(filepath.Join(DefaultDataDir, "chains", "31337", "protocol.sqlite"))
	if cfg.Chains[0].Database.Path != want {
		t.Fatal(cfg)
	}
}
func TestCommandEnvironmentChainSettings(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "env-data")
	t.Setenv("INVISI_RPC_DATA_DIR", dir)
	t.Setenv("INVISI_RPC_DEPLOYMENT_FILE", privateCatalog(t, privateDeployments()))
	t.Setenv("INVISI_RPC_LISTEN", "127.0.0.1:9090")
	t.Setenv("INVISI_RPC_CONFIRMATIONS", "12")
	t.Setenv("INVISI_RPC_SNAPSHOT_INTERVAL", "2h")
	t.Setenv("INVISI_RPC_DATABASE", "sqlite")
	t.Setenv("CHAIN_RPC_URL", "https://localhost:8545?methods=a,b&api=key=value")
	runCommand(t, "invisi-protocol-rpc", "init")
	runCommand(t, "invisi-protocol-rpc", "chain", "add", "31337")
	cfg, e := config.LoadDir(dir)
	if e != nil {
		t.Fatal(e)
	}
	chain := cfg.Chains[0]
	if cfg.Listen != "127.0.0.1:9090" || chain.ChainRPC != "https://localhost:8545?methods=a,b&api=key=value" || chain.Confirmations != 12 || chain.SnapshotInterval != "2h" {
		t.Fatal(cfg)
	}
	t.Setenv("CHAIN_RPC_URL", "wss://localhost:8546/token")
	runCommand(t, "invisi-protocol-rpc", "chain", "set-rpc", "31337")
	cfg, e = config.LoadDir(dir)
	if e != nil || cfg.Chains[0].ChainRPC != "wss://localhost:8546/token" {
		t.Fatal(cfg, e)
	}
}
func TestCommandFlagsOverrideEnvironment(t *testing.T) {
	for _, positional := range []bool{false, true} {
		t.Run(map[bool]string{false: "flag", true: "positional"}[positional], func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "selected")
			unused := filepath.Join(root, "env")
			t.Setenv("INVISI_RPC_DATA_DIR", unused)
			t.Setenv("INVISI_RPC_LISTEN", "127.0.0.1:9090")
			t.Setenv("INVISI_RPC_DEPLOYMENT_FILE", privateCatalog(t, privateDeployments()))
			t.Setenv("CHAIN_RPC_URL", "http://env:8545")
			t.Setenv("INVISI_RPC_CONFIRMATIONS", "12")
			args := []string{"invisi-protocol-rpc", "init", "--listen", "127.0.0.1:8088"}
			if positional {
				args = append(args, dir)
			} else {
				args = append(args, "--dir", dir)
			}
			runCommand(t, args...)
			runCommand(t, "invisi-protocol-rpc", "chain", "add", "31337", "--dir", dir, "--rpc", "http://explicit:8545", "--confirmations", "3")
			cfg, e := config.LoadDir(dir)
			if e != nil || cfg.Listen != "127.0.0.1:8088" || cfg.Chains[0].ChainRPC != "http://explicit:8545" || cfg.Chains[0].Confirmations != 3 {
				t.Fatal(cfg, e)
			}
			if _, e = os.Stat(unused); !os.IsNotExist(e) {
				t.Fatal("created overridden directory")
			}
		})
	}
}

func TestCommandSnapshotExportEnvironment(t *testing.T) {
	t.Setenv("INVISI_RPC_DEPLOYMENT_FILE", privateCatalog(t, privateDeployments()))
	dir := filepath.Join(t.TempDir(), "data")
	out := filepath.Join(t.TempDir(), "exports")
	t.Setenv("INVISI_RPC_DATA_DIR", dir)
	t.Setenv("CHAIN_RPC_URL", "http://localhost:8545")
	runCommand(t, "invisi-protocol-rpc", "init")
	runCommand(t, "invisi-protocol-rpc", "chain", "add", "31337")
	cfg, err := config.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenDatabase(context.Background(), cfg.Chains[0])
	if err != nil {
		t.Fatal(err)
	}
	err = store.Apply(context.Background(), model.Batch{Block: model.Block{Number: cfg.Chains[0].Deployment.FromBlock, Hash: common.HexToHash("0xa")}})
	store.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("INVISI_RPC_CHAIN", "31337")
	t.Setenv("INVISI_RPC_SNAPSHOT_OUTPUT", out)
	path := strings.TrimSpace(runCommand(t, "invisi-protocol-rpc", "snapshot", "export"))
	if !strings.HasPrefix(path, out+string(os.PathSeparator)) {
		t.Fatalf("ignored env output: %s", path)
	}
	inspected := runCommand(t, "invisi-protocol-rpc", "snapshot", "inspect", path)
	var h map[string]any
	if err = json.Unmarshal([]byte(inspected), &h); err != nil {
		t.Fatal(err)
	}
}

func TestCommandRunListenOverride(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "environment", true: "flag"}[explicit], func(t *testing.T) {
			dir := t.TempDir()
			chain := serviceConfig(t)
			chain.Deployment.FromBlock = 0
			chain.Deployment.RuntimeCodeHash = crypto.Keccak256Hash([]byte{0x60, 0x00})
			_, chain.ChainRPC = fixtureChain(t, &chainRPCFixture{latest: 0})
			if err := config.Save(dir, config.Config{Listen: "invalid-saved-listener", Chains: []config.Chain{chain}}); err != nil {
				t.Fatal(err)
			}
			t.Setenv("INVISI_RPC_DATA_DIR", dir)
			listen := unusedListenAddress(t)
			t.Setenv("INVISI_RPC_LISTEN", listen)
			args := []string{"invisi-protocol-rpc", "run"}
			if explicit {
				t.Setenv("INVISI_RPC_LISTEN", "invalid-env-listener")
				args = append(args, "--listen", listen)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- Command(&bytes.Buffer{}).Run(ctx, args) }()
			deadline := time.Now().Add(5 * time.Second)
			for {
				client, err := gethrpc.DialHTTP("http://" + listen + "/v1/chains/31337")
				if err == nil {
					var result any
					callCtx, stop := context.WithTimeout(ctx, 100*time.Millisecond)
					err = client.CallContext(callCtx, &result, "invisi_getVersion")
					stop()
					client.Close()
					if err == nil {
						break
					}
				}
				select {
				case err := <-done:
					t.Fatalf("run failed: %v", err)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("run ignored selected listener")
				}
				time.Sleep(10 * time.Millisecond)
			}
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("run did not stop")
			}
			saved, err := config.LoadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Listen != "invalid-saved-listener" {
				t.Fatal("runtime override modified saved config")
			}
		})
	}
}

func TestCommandDefaultDirectoryDoesNotOverwriteExecutable(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("INVISI_RPC_DATA_DIR", "")
	t.Setenv("CHAIN_RPC_URL", "http://localhost:8545")
	if err := os.WriteFile("invisi-protocol-rpc", []byte("existing executable"), 0700); err != nil {
		t.Fatal(err)
	}
	err := Command(&bytes.Buffer{}).Run(context.Background(), []string{"invisi-protocol-rpc", "init"})
	if err == nil || !strings.Contains(err.Error(), "is a file") {
		t.Fatalf("unclear directory collision: %v", err)
	}
	content, err := os.ReadFile("invisi-protocol-rpc")
	if err != nil || string(content) != "existing executable" {
		t.Fatal("modified existing executable")
	}
}

func TestDetectionSwitchIsIndependentOfCredentials(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "public-enabled", false: "disabled"}[enabled], func(t *testing.T) {
			dir := t.TempDir()
			catalog := privateCatalog(t, privateDeployments())
			t.Setenv("INVISI_RPC_DETECTION_ENABLED", map[bool]string{true: "true", false: "false"}[enabled])
			runCommand(t, "invisi-protocol-rpc", "init", "--dir", dir)
			runCommand(t, "invisi-protocol-rpc", "chain", "add", "--dir", dir, "--deployment-file", catalog, "--rpc", "http://localhost:8545", "31337")
			cfg, err := config.LoadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Chains[0].DetectionEnabled != enabled {
				t.Fatalf("detection configuration lost: %+v", cfg.Chains[0])
			}
		})
	}
}

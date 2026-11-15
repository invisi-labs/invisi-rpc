package app

import (
	"bytes"
	"context"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/config"
	"github.com/urfave/cli/v3"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandDirectoryContract(t *testing.T) {
	for _, mode := range []string{"root", "leaf", "positional", "empty", "empty-positional"} {
		t.Run(mode, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("INVISI_RPC_DATA_DIR", "env-data")
			args := []string{"rpc", "--dir", "root-data", "init"}
			selected := "root-data"
			switch mode {
			case "leaf":
				args = append(args, "--dir", "leaf-data")
				selected = "leaf-data"
			case "positional":
				args = append(args, "pos-data", "--dir", "leaf-data", "--listen", "127.0.0.1:8090")
				selected = "pos-data"
			case "empty-positional":
				args = append(args, "")
				selected = DefaultDataDir
			case "empty":
				args = append(args, "--dir", "")
				selected = DefaultDataDir
			}
			runCommand(t, args...)
			cfg, err := config.LoadDir(selected)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "positional" && cfg.Listen != "127.0.0.1:8090" {
				t.Fatal(cfg)
			}
			if _, err = os.Stat("env-data"); !os.IsNotExist(err) {
				t.Fatal("created environment directory")
			}
		})
	}
}

func TestCommandRejectsInvalidInputBeforeIO(t *testing.T) {
	for _, args := range [][]string{
		{"init", "data.json"}, {"init", "--listen", ""}, {"run", "--listen", ""}, {"init", "--listen", "invalid"},
		{"run", "extra"}, {"snapshot", "export", "extra"},
		{"run", "--listen", "invalid"},
		{"init", "--listen", "127.0.0.1:0"}, {"init", "--listen", "bad/host:8080"}, {"run", "--listen", "bad\\host:8080"}, {"run", "--listen", "bad host:8080"}, {"run", "--listen", "bad\thost:8080"}, {"chain", "wat"}, {"wat"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			t.Chdir(t.TempDir())
			dir := filepath.Join(t.TempDir(), "missing")
			t.Setenv("INVISI_RPC_DATA_DIR", dir)
			command := Command(&bytes.Buffer{})
			command.ExitErrHandler = func(context.Context, *cli.Command, error) {}
			err := command.Run(context.Background(), append([]string{"rpc"}, args...))
			if err == nil {
				t.Fatal("accepted invalid input")
			}
			if strings.Contains(err.Error(), "no such file") {
				t.Fatalf("read config before validating input: %v", err)
			}
			if _, err = os.Stat(dir); !os.IsNotExist(err) {
				t.Fatal("created data directory")
			}
		})
	}
}

func TestCommandHelpAndInvalidSyntaxLeaveDirectoryAbsent(t *testing.T) {
	for _, tail := range [][]string{{}, {"--help"}, {"chain"}, {"snapshot"}, {"chain", "add", "--help"}, {"init", "--unknown"}, {"init", "one", "two"}} {
		t.Run(strings.Join(tail, "_"), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "absent")
			t.Setenv("INVISI_RPC_DATA_DIR", dir)
			command := Command(&bytes.Buffer{})
			command.ExitErrHandler = func(context.Context, *cli.Command, error) {}
			_ = command.Run(context.Background(), append([]string{"rpc"}, tail...))
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("help/invalid syntax created directory: %v", err)
			}
		})
	}
}

func TestCommandDirectoryFlagAtGroupAndSingleHyphen(t *testing.T) {
	dir := initDirectory(t)
	t.Setenv("INVISI_RPC_DATA_DIR", filepath.Join(t.TempDir(), "unused"))
	for _, args := range [][]string{
		{"rpc", "chain", "--dir", dir, "list"},
		{"rpc", "chain", "list", "-dir", dir},
	} {
		if got := strings.TrimSpace(runCommand(t, args...)); got != "[]" {
			t.Fatal(got)
		}
	}
}

func TestCommandEmptyListenEnvironmentKeepsDefault(t *testing.T) {
	t.Setenv("INVISI_RPC_LISTEN", "")
	dir := initDirectory(t)
	cfg, err := config.LoadDir(dir)
	if err != nil || cfg.Listen != "127.0.0.1:8080" {
		t.Fatal(cfg, err)
	}
}

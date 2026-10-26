package model

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestDeploymentUsesSharedContractShape(t *testing.T) {
	original := Deployment{ChainID: "31337", Protocol: common.HexToAddress("1234"), RuntimeCodeHash: common.HexToHash("5678"), FromBlock: 26, Registry: &ContractDeployment{Address: common.HexToAddress("abcd"), RuntimeCodeHash: common.HexToHash("ef01"), FromBlock: 29}}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "protocolAddress") || strings.Contains(string(raw), "registryAddress") {
		t.Fatalf("flat address aliases remain: %s", raw)
	}
	var restored Deployment
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Protocol != original.Protocol || restored.RuntimeCodeHash != original.RuntimeCodeHash || restored.FromBlock != original.FromBlock || restored.Registry == nil || *restored.Registry != *original.Registry {
		t.Fatal("deployment identity lost")
	}
	if err := json.Unmarshal([]byte(`{"chainId":"31337","protocolAddress":"0x0000000000000000000000000000000000001234"}`), &restored); err == nil {
		t.Fatal("legacy flat deployment accepted")
	}
}

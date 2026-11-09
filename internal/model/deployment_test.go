package model

import (
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestDeploymentUsesSharedContractShape(t *testing.T) {
	original := Deployment{ChainID: "31337", Protocol: common.HexToAddress("1234"), RuntimeCodeHash: common.HexToHash("5678"), FromBlock: 26, Registry: &ContractDeployment{Address: common.HexToAddress("abcd"), RuntimeCodeHash: common.HexToHash("ef01"), FromBlock: 29}}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var profile map[string]json.RawMessage
	if err := json.Unmarshal(raw, &profile); err != nil {
		t.Fatal(err)
	}
	if len(profile) != 3 || len(profile["chainId"]) == 0 {
		t.Fatalf("invalid deployment profile: %s", raw)
	}
	for _, name := range []string{"protocol", "registry"} {
		var contract map[string]json.RawMessage
		if err := json.Unmarshal(profile[name], &contract); err != nil {
			t.Fatal(err)
		}
		if len(contract) != 3 || len(contract["address"]) == 0 || len(contract["runtimeCodeHash"]) == 0 || len(contract["fromBlock"]) == 0 {
			t.Fatalf("invalid %s contract shape: %s", name, profile[name])
		}
	}
	var restored Deployment
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Protocol != original.Protocol || restored.RuntimeCodeHash != original.RuntimeCodeHash || restored.FromBlock != original.FromBlock || restored.Registry == nil || *restored.Registry != *original.Registry {
		t.Fatal("deployment identity lost")
	}
	for _, malformed := range []string{`{"chainId":"31337","protocol":[]}`, `{"chainId":31337}`, `{"chainId":"31337","unexpected":true}`} {
		if err := json.Unmarshal([]byte(malformed), &restored); err == nil {
			t.Fatalf("malformed deployment accepted: %s", malformed)
		}
	}
}

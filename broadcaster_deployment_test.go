package deployment

import (
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
)

func TestBroadcasterDeploymentProfileRoundTrip(t *testing.T) {
	p := Profile{DeploymentProfile: model.DeploymentProfile{Protocol: model.ContractDeployment{Address: common.HexToAddress("0x1"), RuntimeCodeHash: common.HexToHash("0xa"), FromBlock: 10}, Registry: &model.ContractDeployment{Address: common.HexToAddress("0x2"), RuntimeCodeHash: common.HexToHash("0xb"), FromBlock: 8}, BroadcasterRegistry: &model.ContractDeployment{Address: common.HexToAddress("0x3"), RuntimeCodeHash: common.HexToHash("0xc"), FromBlock: 6}}}
	d, err := (Catalog{Version: 1, Chains: map[string]Profile{"97": p}}).Resolve("97")
	if err != nil {
		t.Fatal(err)
	}
	if d.IndexFromBlock() != 6 {
		t.Fatal("missed broadcaster registration history")
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var decoded model.Deployment
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.BroadcasterRegistry == nil || *decoded.BroadcasterRegistry != *d.BroadcasterRegistry {
		t.Fatal("lost broadcaster pin")
	}
	p.BroadcasterRegistry = &model.ContractDeployment{Address: p.Protocol.Address, RuntimeCodeHash: common.HexToHash("0xc")}
	if _, err = (Catalog{Version: 1, Chains: map[string]Profile{"97": p}}).Resolve("97"); err == nil {
		t.Fatal("accepted overlapping event emitters")
	}
	p.BroadcasterRegistry = &model.ContractDeployment{Address: common.HexToAddress("0x3")}
	if _, err = (Catalog{Version: 1, Chains: map[string]Profile{"97": p}}).Resolve("97"); err == nil {
		t.Fatal("accepted missing runtime code hash")
	}
}

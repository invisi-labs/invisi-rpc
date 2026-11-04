package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"

	"github.com/ethereum/go-ethereum/common"
)

// ValidateMetadata checks optional registry deployment metadata.
func (d Deployment) ValidateMetadata() error {
	for _, registry := range []*ContractDeployment{d.Registry, d.BroadcasterRegistry} {
		if registry != nil && (registry.Address == (common.Address{}) || registry.RuntimeCodeHash == (common.Hash{}) || registry.FromBlock > math.MaxInt64) {
			return errors.New("invalid registry deployment")
		}
	}
	if d.BroadcasterRegistry != nil && (d.BroadcasterRegistry.Address == d.Protocol || (d.Registry != nil && d.Registry.Address == d.BroadcasterRegistry.Address)) {
		return errors.New("registry event sources must have distinct addresses")
	}
	return nil
}

// DeploymentProfile groups contracts using one deployment record shape.
type DeploymentProfile struct {
	BroadcasterRegistry *ContractDeployment `json:"broadcasterRegistry,omitempty"`
	Protocol            ContractDeployment  `json:"protocol"`
	Registry            *ContractDeployment `json:"registry,omitempty"`
}

func (d Deployment) Profile() DeploymentProfile {
	return DeploymentProfile{Protocol: ContractDeployment{Address: d.Protocol, RuntimeCodeHash: d.RuntimeCodeHash, FromBlock: d.FromBlock}, Registry: d.Registry, BroadcasterRegistry: d.BroadcasterRegistry}
}

// MarshalJSON exposes the same profile in configuration, snapshots and RPC responses.
func (d Deployment) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ChainID string `json:"chainId"`
		DeploymentProfile
	}{d.ChainID, d.Profile()})
}

func (d *Deployment) UnmarshalJSON(data []byte) error {
	var value struct {
		ChainID string `json:"chainId"`
		DeploymentProfile
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	*d = Deployment{ChainID: value.ChainID, Protocol: value.Protocol.Address, RuntimeCodeHash: value.Protocol.RuntimeCodeHash, FromBlock: value.Protocol.FromBlock, Registry: value.Registry, BroadcasterRegistry: value.BroadcasterRegistry}
	return nil
}

// IndexFromBlock covers all independently deployed event sources.
func (d Deployment) IndexFromBlock() uint64 {
	start := d.FromBlock
	for _, registry := range []*ContractDeployment{d.Registry, d.BroadcasterRegistry} {
		if registry != nil && registry.FromBlock < start {
			start = registry.FromBlock
		}
	}
	return start
}

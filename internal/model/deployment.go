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
	if d.Registry != nil && (d.Registry.Address == (common.Address{}) || d.Registry.RuntimeCodeHash == (common.Hash{}) || d.Registry.FromBlock > math.MaxInt64) {
		return errors.New("invalid registry deployment")
	}
	return nil
}

// DeploymentProfile groups contracts using one deployment record shape.
type DeploymentProfile struct {
	Protocol ContractDeployment  `json:"protocol"`
	Registry *ContractDeployment `json:"registry,omitempty"`
}

func (d Deployment) Profile() DeploymentProfile {
	return DeploymentProfile{Protocol: ContractDeployment{Address: d.Protocol, RuntimeCodeHash: d.RuntimeCodeHash, FromBlock: d.FromBlock}, Registry: d.Registry}
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
	*d = Deployment{ChainID: value.ChainID, Protocol: value.Protocol.Address, RuntimeCodeHash: value.Protocol.RuntimeCodeHash, FromBlock: value.Protocol.FromBlock, Registry: value.Registry}
	return nil
}

package protocol

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/cryptography"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
)

// Generated from contracts/out/InvisiAddressRegistry.sol/InvisiAddressRegistry.json.
//
//go:embed InvisiAddressRegistry.abi
var registryABI []byte

// DeploymentDecoder keeps event ABIs bound to their configured emitting contract.
type DeploymentDecoder struct {
	protocol   *Decoder
	registry   abi.ABI
	deployment model.Deployment
}

func NewDeploymentDecoder(deployment model.Deployment) (*DeploymentDecoder, error) {
	p, err := NewDecoder()
	if err != nil {
		return nil, err
	}
	r, err := abi.JSON(bytes.NewReader(registryABI))
	if err != nil {
		return nil, err
	}
	return &DeploymentDecoder{protocol: p, registry: r, deployment: deployment}, nil
}
func (d *DeploymentDecoder) Decode(block model.Block, logs []types.Log) (model.Batch, error) {
	var poolLogs, registryLogs []types.Log
	for _, log := range logs {
		switch {
		case log.Address == d.deployment.Protocol && block.Number >= d.deployment.FromBlock:
			poolLogs = append(poolLogs, log)
		case d.deployment.Registry != nil && log.Address == d.deployment.Registry.Address && block.Number >= d.deployment.Registry.FromBlock:
			registryLogs = append(registryLogs, log)
		default:
			return model.Batch{}, errors.New("event emitter or deployment height mismatch")
		}
	}
	batch, err := d.protocol.Decode(block, poolLogs)
	if err != nil {
		return model.Batch{}, err
	}
	batch.Logs = append([]types.Log(nil), logs...)
	event := d.registry.Events["ReceivingKeysSet"]
	sort.Slice(registryLogs, func(i, j int) bool { return registryLogs[i].Index < registryLogs[j].Index })
	for _, log := range registryLogs {
		if log.Removed {
			return model.Batch{}, errors.New("removed registry log")
		}
		if len(log.Topics) == 0 || log.Topics[0] != event.ID {
			continue
		}
		if len(log.Topics) != 2 {
			return model.Batch{}, errors.New("invalid registry topic count")
		}
		identity := log.Topics[1]
		owner := common.BytesToAddress(identity[:])
		if owner == (common.Address{}) || common.BytesToHash(owner.Bytes()) != identity {
			return model.Batch{}, errors.New("invalid registry wallet identity")
		}
		values, err := event.Inputs.NonIndexed().Unpack(log.Data)
		if err != nil {
			return model.Batch{}, fmt.Errorf("decode registry event: %w", err)
		}
		viewingKey := values[0].([]byte)
		clueKey := common.Hash(values[1].([32]byte))
		nonce := values[2].(uint64)
		if _, err = crypto.DecompressPubkey(viewingKey); err != nil {
			return model.Batch{}, errors.New("invalid registry viewing key")
		}
		if err = cryptography.ValidateClueKey(clueKey[:]); err != nil {
			return model.Batch{}, err
		}
		if nonce == 0 {
			return model.Batch{}, errors.New("invalid registry nonce")
		}
		update := model.RegistrationUpdate{WalletIdentity: owner, ViewingPublicKey: viewingKey, ClueKey: clueKey, Nonce: nonce, LogIndex: log.Index}
		batch.Registrations = append(batch.Registrations, update)
		raw, err := json.Marshal(update)
		if err != nil {
			return model.Batch{}, err
		}
		batch.Events = append(batch.Events, model.Event{Block: block, Name: event.Name, TransactionHash: log.TxHash, LogIndex: log.Index, Data: raw})
	}
	sort.SliceStable(batch.Events, func(i, j int) bool { return batch.Events[i].LogIndex < batch.Events[j].LogIndex })
	return batch, nil
}

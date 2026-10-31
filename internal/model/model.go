package model

import (
	"encoding/json"
	"errors"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
)

type ContractDeployment struct {
	Address         common.Address `json:"address"`
	RuntimeCodeHash common.Hash    `json:"runtimeCodeHash"`
	FromBlock       uint64         `json:"fromBlock"`
}

type Deployment struct {
	Registry        *ContractDeployment `json:"registry,omitempty"`
	ChainID         string              `json:"chainId"`
	Protocol        common.Address      `json:"protocolAddress"`
	FromBlock       uint64              `json:"fromBlock"`
	RuntimeCodeHash common.Hash         `json:"runtimeCodeHash"`
}
type Block struct {
	Timestamp  uint64      `json:"timestamp"`
	Number     uint64      `json:"number"`
	Hash       common.Hash `json:"hash"`
	ParentHash common.Hash `json:"parentHash"`
}

// UnmarshalJSON requires the current block timestamp field; explicit epoch zero is valid.
func (b *Block) UnmarshalJSON(data []byte) error {
	var required struct {
		Timestamp *uint64 `json:"timestamp"`
	}
	if err := json.Unmarshal(data, &required); err != nil {
		return err
	}
	if required.Timestamp == nil {
		return errors.New("block timestamp is required")
	}
	type block Block
	var value block
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*b = Block(value)
	return nil
}

type Insertion struct {
	TreeID      uint64        `json:"treeId"`
	Start       uint32        `json:"startLeafIndex"`
	Commitments []common.Hash `json:"commitments"`
	LogIndex    uint          `json:"logIndex"`
}
type Output struct {
	Block           Block          `json:"block"`
	TreeID          uint64         `json:"treeId"`
	LeafIndex       uint32         `json:"leafIndex"`
	Commitment      common.Hash    `json:"commitment"`
	Token           common.Address `json:"token"`
	Ciphertext      hexutil.Bytes  `json:"ciphertext"`
	Clue            hexutil.Bytes  `json:"clue"`
	TransactionHash common.Hash    `json:"transactionHash"`
	LogIndex        uint           `json:"logIndex"`
	Kind            string         `json:"kind"`
}
type Event struct {
	Block           Block           `json:"block"`
	Name            string          `json:"name"`
	TransactionHash common.Hash     `json:"transactionHash"`
	LogIndex        uint            `json:"logIndex"`
	Data            json.RawMessage `json:"data"`
}
type RegistrationUpdate struct {
	WalletIdentity   common.Address `json:"walletIdentity"`
	ViewingPublicKey hexutil.Bytes  `json:"viewingPublicKey"`
	ClueKey          common.Hash    `json:"clueKey"`
	Nonce            uint64         `json:"nonce,string"`
	LogIndex         uint           `json:"logIndex"`
}

// RegisteredReceivingAddress reflects keys through the returned indexed block.
type RegisteredReceivingAddress struct {
	WalletIdentity   common.Address `json:"walletIdentity"`
	ViewingPublicKey hexutil.Bytes  `json:"viewingPublicKey"`
	ClueKey          common.Hash    `json:"clueKey"`
	Nonce            uint64         `json:"nonce,string"`
	Block            Block          `json:"block"`
}
type Batch struct {
	Registrations []RegistrationUpdate `json:"registrations,omitempty"`
	Block         Block                `json:"block"`
	Logs          []types.Log          `json:"logs"`
	Events        []Event              `json:"events"`
	CreatedTrees  []uint64             `json:"createdTrees"`
	Insertions    []Insertion          `json:"insertions"`
	Outputs       []Output             `json:"outputs"`
	Nullifiers    []common.Hash        `json:"nullifiers"`
}
type Root struct {
	TreeID    uint64      `json:"treeId"`
	LeafCount uint32      `json:"leafCount"`
	Root      common.Hash `json:"root"`
	Block     Block       `json:"block"`
}
type Membership struct {
	Root       Root          `json:"anchor"`
	LeafIndex  uint32        `json:"leafIndex"`
	Commitment common.Hash   `json:"commitment"`
	Siblings   []common.Hash `json:"siblings"`
}

// BindBlockContext associates every candidate and transition with its indexed
// canonical header. Neither ciphertext nor decoded event data supplies time.
func (b *Batch) BindBlockContext() {
	for i := range b.Events {
		b.Events[i].Block = b.Block
	}
	for i := range b.Outputs {
		b.Outputs[i].Block = b.Block
	}
}

// UnmarshalJSON requires a current block record for persisted and snapshot batches.
func (b *Batch) UnmarshalJSON(data []byte) error {
	var required struct {
		Block *Block `json:"block"`
	}
	if err := json.Unmarshal(data, &required); err != nil {
		return err
	}
	if required.Block == nil {
		return errors.New("batch block is required")
	}
	type batch Batch
	var value batch
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*b = Batch(value)
	return nil
}

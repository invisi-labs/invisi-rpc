package model

import (
	"encoding/json"
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
	Number     uint64      `json:"number"`
	Hash       common.Hash `json:"hash"`
	ParentHash common.Hash `json:"parentHash"`
}
type Insertion struct {
	TreeID      uint64        `json:"treeId"`
	Start       uint32        `json:"startLeafIndex"`
	Commitments []common.Hash `json:"commitments"`
	LogIndex    uint          `json:"logIndex"`
}
type Output struct {
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
	Name            string          `json:"name"`
	TransactionHash common.Hash     `json:"transactionHash"`
	LogIndex        uint            `json:"logIndex"`
	Data            json.RawMessage `json:"data"`
}
type Batch struct {
	Block        Block         `json:"block"`
	Logs         []types.Log   `json:"logs"`
	Events       []Event       `json:"events"`
	CreatedTrees []uint64      `json:"createdTrees"`
	Insertions   []Insertion   `json:"insertions"`
	Outputs      []Output      `json:"outputs"`
	Nullifiers   []common.Hash `json:"nullifiers"`
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

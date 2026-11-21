package protocol

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Retired shard/slot events must never be silently dropped from a new index.
var legacyCreditTopics = map[common.Hash]bool{
	crypto.Keccak256Hash([]byte("CreditCreated(address,address,uint256,uint64,uint16,bytes32,uint32,(bytes,bytes)[],uint256,address,uint256)")):                                                       true,
	crypto.Keccak256Hash([]byte("CreditRevoked(address,address,uint64,uint16,address,uint256)")):                                                                                                      true,
	crypto.Keccak256Hash([]byte("CreditStateTransition(uint256,bytes32,((uint256,bytes32)[],(address,uint256)[],(uint256,uint64,uint16)[],bytes32[],bytes32[],bytes32[]),uint32[],(bytes,bytes)[])")): true,
	crypto.Keccak256Hash([]byte("CreditUpdated(address,uint256,(address,address,(uint256,bytes32),bytes32,bytes32,uint64,uint16,uint256,address,uint256),uint32,(bytes,bytes)[])")):                   true,
}

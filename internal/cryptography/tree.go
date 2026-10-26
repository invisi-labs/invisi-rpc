package cryptography

import (
	"fmt"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/common"
)

const TreeDepth = 16

// HashPair hashes canonical big-endian BN254 scalar field encodings. Rejecting
// noncanonical inputs prevents silently reducing corrupt persisted commitments.
func HashPair(left, right common.Hash) (common.Hash, error) {
	var a, b fr.Element
	if err := a.SetBytesCanonical(left[:]); err != nil {
		return common.Hash{}, fmt.Errorf("left node is not canonical: %w", err)
	}
	if err := b.SetBytesCanonical(right[:]); err != nil {
		return common.Hash{}, fmt.Errorf("right node is not canonical: %w", err)
	}
	h, err := Poseidon2Hash(a, b)
	if err != nil {
		return common.Hash{}, err
	}
	return common.Hash(h.Bytes()), nil
}

// EmptyNodes matches IncrementalMerkleTree.sol: level zero is the empty leaf,
// and level sixteen is its virtual genesis root.
var EmptyNodes = func() [TreeDepth + 1]common.Hash {
	var nodes [TreeDepth + 1]common.Hash
	for i := 1; i <= TreeDepth; i++ {
		var err error
		nodes[i], err = HashPair(nodes[i-1], nodes[i-1])
		if err != nil {
			panic(err)
		}
	}
	return nodes
}()

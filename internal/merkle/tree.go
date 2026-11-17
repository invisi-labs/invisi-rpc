// Package merkle computes append-only depth-16 protocol trees. Storage selects
// the version seen by a NodeReader, keeping computation independent of a database.
package merkle

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-rpc/internal/cryptography"
)

const Depth = cryptography.TreeDepth
const Capacity uint32 = 1 << Depth

var EmptyNodes = cryptography.EmptyNodes

// NodeReader returns a node from a single consistent persisted tree version.
// Level zero contains leaves; level Depth contains the root. Missing occupied
// nodes are errors, rather than being silently treated as empty subtrees.
type NodeReader func(level uint8, index uint32) (common.Hash, bool, error)
type Node struct {
	Level uint8
	Index uint32
	Hash  common.Hash
}

func sibling(level uint8, index, count uint32, read NodeReader) (common.Hash, error) {
	if uint64(index)<<level >= uint64(count) {
		return EmptyNodes[level], nil
	}
	if read == nil {
		return common.Hash{}, fmt.Errorf("missing node reader")
	}
	h, ok, err := read(level, index)
	if err != nil {
		return common.Hash{}, err
	}
	if !ok {
		return common.Hash{}, fmt.Errorf("missing occupied node at level %d index %d", level, index)
	}
	return h, nil
}

// Insert returns all changed nodes, including the leaf and root. Persist them
// atomically with the new leaf count and version after validating the chain root.
func Insert(nextLeafIndex uint32, leaf common.Hash, read NodeReader) (common.Hash, []Node, error) {
	if nextLeafIndex >= Capacity {
		return common.Hash{}, nil, fmt.Errorf("merkle tree full")
	}
	current, index := leaf, nextLeafIndex
	nodes := []Node{{Level: 0, Index: index, Hash: current}}
	for level := uint8(0); level < Depth; level++ {
		other, err := sibling(level, index^1, nextLeafIndex, read)
		if err != nil {
			return common.Hash{}, nil, err
		}
		if index&1 == 0 {
			current, err = cryptography.HashPair(current, other)
		} else {
			current, err = cryptography.HashPair(other, current)
		}
		if err != nil {
			return common.Hash{}, nil, err
		}
		index >>= 1
		nodes = append(nodes, Node{Level: level + 1, Index: index, Hash: current})
	}
	return current, nodes, nil
}

// Path returns bottom-up siblings for a leaf present at the reader's version.
func Path(leafIndex, leafCount uint32, read NodeReader) ([Depth]common.Hash, error) {
	var path [Depth]common.Hash
	if leafCount > Capacity || leafIndex >= leafCount {
		return path, fmt.Errorf("leaf index outside tree version")
	}
	index := leafIndex
	for level := uint8(0); level < Depth; level++ {
		h, err := sibling(level, index^1, leafCount, read)
		if err != nil {
			return path, err
		}
		path[level] = h
		index >>= 1
	}
	return path, nil
}

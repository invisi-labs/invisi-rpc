package merkle

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-rpc/internal/cryptography"
)

type coordinate struct {
	level uint8
	index uint32
}

func TestHistoricalPaths(t *testing.T) {
	snapshots := []map[coordinate]common.Hash{{}}
	roots := []common.Hash{EmptyNodes[Depth]}
	for n := uint32(0); n < 19; n++ {
		previous := snapshots[len(snapshots)-1]
		read := func(l uint8, i uint32) (common.Hash, bool, error) {
			h, ok := previous[coordinate{l, i}]
			return h, ok, nil
		}
		root, nodes, err := Insert(n, common.BigToHash(big.NewInt(int64(n+1))), read)
		if err != nil {
			t.Fatal(err)
		}
		next := make(map[coordinate]common.Hash, len(previous)+17)
		for k, v := range previous {
			next[k] = v
		}
		for _, node := range nodes {
			next[coordinate{node.Level, node.Index}] = node.Hash
		}
		snapshots = append(snapshots, next)
		roots = append(roots, root)
	}
	for version := 1; version < len(snapshots); version++ {
		snapshot := snapshots[version]
		read := func(l uint8, i uint32) (common.Hash, bool, error) {
			h, ok := snapshot[coordinate{l, i}]
			return h, ok, nil
		}
		for leaf := uint32(0); leaf < uint32(version); leaf++ {
			path, err := Path(leaf, uint32(version), read)
			if err != nil {
				t.Fatal(err)
			}
			h := snapshot[coordinate{0, leaf}]
			index := leaf
			for _, sibling := range path {
				if index&1 == 0 {
					h, err = cryptography.HashPair(h, sibling)
				} else {
					h, err = cryptography.HashPair(sibling, h)
				}
				if err != nil {
					t.Fatal(err)
				}
				index >>= 1
			}
			if h != roots[version] {
				t.Fatalf("version %d leaf %d root mismatch", version, leaf)
			}
		}
	}
}
func TestBoundsAndMissingNodes(t *testing.T) {
	if _, _, err := Insert(Capacity, common.Hash{}, nil); err == nil {
		t.Fatal("full tree accepted")
	}
	if _, _, err := Insert(1, common.Hash{}, nil); err == nil {
		t.Fatal("missing reader accepted")
	}
	absent := func(uint8, uint32) (common.Hash, bool, error) { return common.Hash{}, false, nil }
	if _, err := Path(0, 2, absent); err == nil {
		t.Fatal("missing sibling accepted")
	}
	if _, err := Path(1, 1, absent); err == nil {
		t.Fatal("future leaf accepted")
	}
	sentinel := errors.New("database failure")
	broken := func(uint8, uint32) (common.Hash, bool, error) { return common.Hash{}, false, sentinel }
	if _, _, err := Insert(1, common.Hash{}, broken); !errors.Is(err, sentinel) {
		t.Fatalf("lost read failure: %v", err)
	}
}

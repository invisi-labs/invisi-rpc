package protocol

import (
	"bytes"
	"encoding/binary"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-lab/invisi-rpc/internal/model"
)

func memoSegment(size int, marker byte) []byte {
	cipher := bytes.Repeat([]byte{marker}, size)
	cipher[0] = 2
	clue := bytes.Repeat([]byte{marker + 1}, 68)
	clue[64] = 24
	segment := make([]byte, 2, 2+size+68)
	binary.BigEndian.PutUint16(segment, uint16(size))
	segment = append(segment, cipher...)
	return append(segment, clue...)
}

func TestMemoBatchOutputsMixedLengths(t *testing.T) {
	d, log := fixture(t)
	sizes := []int{160, 177, 416}
	var packed []byte
	tokens := []common.Address{{1}, {2}, {3}}
	commitments := [][32]byte{{1}, {2}, {3}}
	for i, size := range sizes {
		packed = append(packed, memoSegment(size, byte(i+10))...)
	}
	l := log("BatchOutputs", 1, common.Hash{}, []any{big.NewInt(2)}, uint32(91), big.NewInt(5), tokens, commitments, packed)
	b, err := d.Decode(model.Block{}, []types.Log{l})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Outputs) != 3 {
		t.Fatalf("got %d outputs", len(b.Outputs))
	}
	for i, output := range b.Outputs {
		if len(output.Ciphertext) != sizes[i] || output.Ciphertext[0] != 2 || output.Ciphertext[1] != byte(i+10) || len(output.Clue) != 68 || output.Clue[0] != byte(i+11) || output.Token != tokens[i] || output.Commitment != commitments[i] || output.LeafIndex != uint32(91+i) {
			t.Fatalf("output %d lost its envelope, clue or position", i)
		}
	}
	packed[3] ^= 255
	if b.Outputs[0].Ciphertext[1] != 10 {
		t.Fatal("decoded ciphertext aliases input")
	}
}

func TestMemoBatchOutputsRejectMalformedFraming(t *testing.T) {
	d, log := fixture(t)
	valid := memoSegment(160, 9)
	wrongPrecision := append([]byte(nil), valid...)
	wrongPrecision[2+160+64] = 8
	malformed := map[string][]byte{
		"empty":            nil,
		"truncated prefix": {0},
		"overlong cipher":  memoSegment(4097, 9),

		"truncated clue": valid[:len(valid)-1],
		"missing output": valid,
		"trailing bytes": append(append([]byte(nil), valid...), 1),

		"wrong precision": wrongPrecision,
	}
	for name, packed := range malformed {
		t.Run(name, func(t *testing.T) {
			count := 1
			if name == "missing output" {
				count = 2
			}
			l := log("BatchOutputs", 1, common.Hash{}, []any{big.NewInt(2)}, uint32(91), big.NewInt(0), make([]common.Address, count), make([][32]byte, count), packed)
			if _, err := d.Decode(model.Block{}, []types.Log{l}); err == nil {
				t.Fatal("accepted malformed packed deliveries")
			}
		})
	}
}

func TestUnifiedProtocolVersion(t *testing.T) {
	if SupportedVersion != "1" || APIPrefix != "/v1" {
		t.Fatal("the shared protocol release must remain v1")
	}
}

func TestOpaqueCiphertextBatchIgnoresCodecVersions(t *testing.T) {
	d, log := fixture(t)
	var packed []byte
	sizes := []int{1, 161, 502, 754, 4096}
	for _, size := range sizes {
		segment := memoSegment(size, 9)
		segment[2] = 0x7f
		packed = append(packed, segment...)
	}
	l := log("BatchOutputs", 1, common.Hash{}, []any{big.NewInt(2)}, uint32(91), big.NewInt(0), make([]common.Address, len(sizes)), make([][32]byte, len(sizes)), packed)
	b, err := d.Decode(model.Block{}, []types.Log{l})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Outputs) != len(sizes) {
		t.Fatal("missing outputs")
	}
	for i, size := range sizes {
		if len(b.Outputs[i].Ciphertext) != size || b.Outputs[i].LeafIndex != uint32(91+i) {
			t.Fatal("lost framing or leaf index")
		}
	}
	for _, size := range []int{0, 4097} {
		segment := make([]byte, 2+size+68)
		binary.BigEndian.PutUint16(segment, uint16(size))
		l = log("BatchOutputs", 1, common.Hash{}, []any{big.NewInt(2)}, uint32(91), big.NewInt(0), make([]common.Address, 1), make([][32]byte, 1), segment)
		if _, err := d.Decode(model.Block{}, []types.Log{l}); err == nil {
			t.Fatal("accepted invalid resource size")
		}
	}
}

func TestBatchOutputsWithoutEnvelopePreservePositions(t *testing.T) {
	d, log := fixture(t)
	packed := append([]byte{0, 0}, memoSegment(160, 9)...)
	packed = append(packed, 0, 0)
	commitments := [][32]byte{{1}, {2}, {3}}
	l := log("BatchOutputs", 1, common.Hash{}, []any{big.NewInt(2)}, uint32(91), big.NewInt(0), make([]common.Address, 3), commitments, packed)
	b, err := d.Decode(model.Block{}, []types.Log{l})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Outputs) != 3 {
		t.Fatalf("lost outputs: %d", len(b.Outputs))
	}
	for i, output := range b.Outputs {
		if output.Commitment != commitments[i] || output.LeafIndex != uint32(91+i) {
			t.Fatalf("lost position %d", i)
		}
		if i != 1 && (len(output.Ciphertext) != 0 || len(output.Clue) != 0) {
			t.Fatalf("unexpected envelope %d", i)
		}
	}
	if len(b.Outputs[1].Ciphertext) != 160 || len(b.Outputs[1].Clue) != 68 {
		t.Fatal("lost adjacent encrypted delivery")
	}
}

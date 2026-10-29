package protocol

import (
	"bytes"
	"encoding/binary"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
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
	wrongVersion := append([]byte(nil), valid...)
	wrongVersion[2] = 1
	wrongPrecision := append([]byte(nil), valid...)
	wrongPrecision[2+160+64] = 8
	malformed := map[string][]byte{
		"empty":            nil,
		"truncated prefix": {0},
		"short cipher":     memoSegment(159, 9),
		"overlong cipher":  memoSegment(417, 9),
		"truncated clue":   valid[:len(valid)-1],
		"missing output":   valid,
		"trailing bytes":   append(append([]byte(nil), valid...), 1),
		"retired version":  wrongVersion,
		"wrong precision":  wrongPrecision,
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

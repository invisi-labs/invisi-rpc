package protocol

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-rpc/internal/model"
)

type delivery struct{ Ciphertext, Clue []byte }

func fixture(t *testing.T) (*Decoder, func(string, uint, common.Hash, []any, ...any) types.Log) {
	t.Helper()
	d, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	return d, func(name string, index uint, tx common.Hash, indexed []any, args ...any) types.Log {
		t.Helper()
		e := d.abi.Events[name]
		data, err := e.Inputs.NonIndexed().Pack(args...)
		if err != nil {
			t.Fatal(err)
		}
		topics := []common.Hash{e.ID}
		for _, v := range indexed {
			q, err := abi.MakeTopics([]any{v})
			if err != nil {
				t.Fatal(err)
			}
			topics = append(topics, q[0][0])
		}
		return types.Log{Topics: topics, Data: data, Index: index, TxHash: tx}
	}
}

type sourceTuple struct {
	TreeId     *big.Int
	MerkleRoot [32]byte
}

// noteFixture emits one unified Shield operation with a single insertion range.
func noteFixture(t *testing.T, treeID *big.Int, start uint32, commitments [][32]byte, packed []byte) (*Decoder, []types.Log) {
	t.Helper()
	d, log := fixture(t)
	id := common.HexToHash("1234")
	tx := common.HexToHash("55")
	token := common.HexToAddress("11")
	return d, []types.Log{
		log("NoteOperation", 0, tx, []any{id, token}, uint8(0), big.NewInt(int64(len(commitments))), big.NewInt(99), common.Address{}, new(big.Int)),
		log("PublicDeposit", 1, tx, []any{id, token, common.HexToAddress("22")}, big.NewInt(5)),
		log("CommitmentsInserted", 2, tx, []any{treeID}, start, commitments),
		log("NoteOutputs", 3, tx, []any{id, treeID}, start, packed),
	}
}
func TestUnifiedCorrelationAndInsertionBoundaries(t *testing.T) {
	d, logs := noteFixture(t, big.NewInt(7), 12, [][32]byte{{1}}, memoSegment(160, 9))
	b, err := d.Decode(model.Block{}, logs)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Insertions) != 1 || len(b.Outputs) != 1 || b.Outputs[0].LeafIndex != 12 || b.Outputs[0].OperationID != common.HexToHash("1234") {
		t.Fatalf("projection: %+v", b)
	}
	for _, mutate := range []func([]types.Log){
		func(ls []types.Log) { ls[3].TxHash = common.HexToHash("77") },
		func(ls []types.Log) {
			ls[3].Topics = append([]common.Hash(nil), ls[3].Topics...)
			ls[3].Topics[1] = common.HexToHash("77")
		},
		func(ls []types.Log) { ls[3].Removed = true },
	} {
		copyLogs := append([]types.Log(nil), logs...)
		mutate(copyLogs)
		if _, err = d.Decode(model.Block{}, copyLogs); err == nil {
			t.Fatal("accepted broken receipt correlation")
		}
	}
	d, logs = noteFixture(t, big.NewInt(1), 65535, [][32]byte{{1}, {2}}, append(memoSegment(160, 1), memoSegment(160, 2)...))
	if _, err = d.Decode(model.Block{}, logs); err == nil {
		t.Fatal("accepted overflowing insertion range")
	}
}

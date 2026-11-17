package protocol

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-lab/invisi-rpc/internal/model"
)

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
func TestShieldCorrelationAndInsertionBoundaries(t *testing.T) {
	d, log := fixture(t)
	tx := common.HexToHash("01")
	c := [32]byte{1}
	token := common.HexToAddress("01")
	deliveryTuple := []delivery{{[]byte{1, 2}, []byte{3}}}
	ins := log("CommitmentsInserted", 1, tx, []any{big.NewInt(7)}, uint32(12), [][32]byte{c})
	shield := log("Shield", 2, tx, []any{token, common.Address{}, big.NewInt(7)}, big.NewInt(1), uint32(12), deliveryTuple, big.NewInt(9))
	b, err := d.Decode(model.Block{}, []types.Log{shield, ins})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Insertions) != 1 || len(b.Outputs) != 1 || b.Outputs[0].Commitment != c || b.Outputs[0].LeafIndex != 12 {
		t.Fatalf("unexpected decoded batch: %+v", b)
	}
	shield.TxHash = common.HexToHash("02")
	if _, err = d.Decode(model.Block{}, []types.Log{ins, shield}); err == nil {
		t.Fatal("accepted insertion from different transaction")
	}
}
func TestBatchOutputsPackedDeliveries(t *testing.T) {
	d, log := fixture(t)
	tx := common.Hash{}
	p := append(memoSegment(160, 6), memoSegment(416, 7)...)
	p[2+160] = 9
	l := log("BatchOutputs", 1, tx, []any{big.NewInt(1)}, uint32(2), big.NewInt(0), []common.Address{{1}, {2}}, [][32]byte{{1}, {2}}, p)
	b, err := d.Decode(model.Block{}, []types.Log{l})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Insertions) != 1 || len(b.Outputs) != 2 || len(b.Outputs[0].Ciphertext) != 160 || b.Outputs[0].Clue[0] != 9 || b.Outputs[1].Ciphertext[1] != 7 {
		t.Fatal("incorrect packed delivery split")
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(b.Events[0].Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data["noteCommitments"]) == 0 || len(data["commitments"]) != 0 {
		t.Fatal("BatchOutputs must name ordinary Note commitments explicitly")
	}
	bad := log("BatchOutputs", 1, tx, []any{big.NewInt(1)}, uint32(2), big.NewInt(0), []common.Address{{1}}, [][32]byte{{1}, {2}}, p)
	if _, err = d.Decode(model.Block{}, []types.Log{bad}); err == nil {
		t.Fatal("accepted inconsistent lengths")
	}
}

type sourceTuple struct {
	TreeId     *big.Int
	MerkleRoot [32]byte
}

func TestTransactAndBatchRequestNullifiers(t *testing.T) {
	d, log := fixture(t)
	tx := common.Hash{}
	ins := log("CommitmentsInserted", 1, tx, []any{big.NewInt(2)}, uint32(6), [][32]byte{{3}})
	tr := log("Transact", 2, tx, []any{big.NewInt(1), big.NewInt(2)}, common.Address{}, [][32]byte{{4}}, [][32]byte{{3}}, []uint32{6}, []delivery{{[]byte{1}, nil}}, common.Address{}, big.NewInt(0), big.NewInt(9))
	b, err := d.Decode(model.Block{}, []types.Log{ins, tr})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Outputs) != 1 || len(b.Insertions) != 1 || len(b.Nullifiers) != 1 {
		t.Fatalf("incorrect transact %+v", b)
	}
	req := log("BatchRequest", 3, tx, []any{big.NewInt(0)}, common.Address{}, big.NewInt(2), []sourceTuple{{big.NewInt(1), [32]byte{1}}}, [][32]byte{{5}}, big.NewInt(0), common.Address{}, big.NewInt(0), common.Address{}, big.NewInt(0), big.NewInt(9), [32]byte{6})
	b, err = d.Decode(model.Block{}, []types.Log{req})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Nullifiers) != 1 || len(b.Outputs) != 0 || len(b.Events) != 1 {
		t.Fatalf("incorrect batch request %+v", b)
	}
}

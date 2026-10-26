package protocol

import (
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
	"math/big"
	"testing"
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
	p := make([]byte, 452)
	p[158] = 9
	p[226] = 7
	l := log("BatchOutputs", 1, tx, []any{big.NewInt(1)}, uint32(2), big.NewInt(0), []common.Address{{1}, {2}}, [][32]byte{{1}, {2}}, p)
	b, err := d.Decode(model.Block{}, []types.Log{l})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Insertions) != 1 || len(b.Outputs) != 2 || len(b.Outputs[0].Ciphertext) != 158 || b.Outputs[0].Clue[0] != 9 || b.Outputs[1].Ciphertext[0] != 7 {
		t.Fatal("incorrect packed delivery split")
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
type drawTuple struct {
	Token                common.Address
	Sources              []sourceTuple
	Nullifiers           [][32]byte
	RemainderCommitments [][32]byte
	Expiries             []*big.Int
	RecoveryAmounts      [][32]byte
	Spender              common.Address
	TotalAmount          *big.Int
	Deadline             *big.Int
}

func TestCreditDrawPairedAndEmptyRemainders(t *testing.T) {
	d, log := fixture(t)
	tx := common.Hash{}
	p := drawTuple{Sources: []sourceTuple{{big.NewInt(1), [32]byte{9}}}, Nullifiers: [][32]byte{{3}, {4}}, RemainderCommitments: [][32]byte{{5}, {}}, Expiries: []*big.Int{big.NewInt(9), big.NewInt(9)}, RecoveryAmounts: [][32]byte{{}, {}}, TotalAmount: big.NewInt(1), Deadline: big.NewInt(9)}
	ins := log("CommitmentsInserted", 1, tx, []any{big.NewInt(2)}, uint32(10), [][32]byte{{5}})
	draw := log("CreditDraw", 2, tx, []any{common.Address{}, big.NewInt(2)}, p, []uint32{10, 0}, []delivery{{[]byte{1}, []byte{2}}, {[]byte{3}, []byte{4}}, {}, {}})
	b, err := d.Decode(model.Block{}, []types.Log{ins, draw})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Outputs) != 2 || len(b.Nullifiers) != 2 || b.Outputs[0].Commitment != b.Outputs[1].Commitment || len(b.Insertions) != 1 {
		t.Fatalf("incorrect credit batch %+v", b)
	}
	draw.Data = draw.Data[:32]
	if _, err = d.Decode(model.Block{}, []types.Log{ins, draw}); err == nil {
		t.Fatal("accepted truncated ABI tuple")
	}
}
func TestCreditCreatedOwnerSpenderAndChange(t *testing.T) {
	d, log := fixture(t)
	tx := common.Hash{}
	ins := log("CommitmentsInserted", 1, tx, []any{big.NewInt(1)}, uint32(0), [][32]byte{{1}, {2}})
	created := log("CreditCreated", 2, tx, []any{common.Address{}, big.NewInt(1)}, common.Address{}, []sourceTuple{{big.NewInt(1), [32]byte{4}}}, [][32]byte{{3}}, [32]byte{1}, [32]byte{2}, []uint32{0, 1}, []delivery{{[]byte{1}, nil}, {[]byte{2}, nil}, {[]byte{3}, nil}}, big.NewInt(99))
	b, err := d.Decode(model.Block{}, []types.Log{ins, created})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Outputs) != 3 || b.Outputs[0].LeafIndex != b.Outputs[1].LeafIndex || b.Outputs[2].LeafIndex != 1 {
		t.Fatal("incorrect owner/spender/change mapping")
	}
}

type creationTuple struct {
	Token            common.Address
	Sources          []sourceTuple
	Nullifiers       [][32]byte
	CreditCommitment [32]byte
	ChangeCommitment [32]byte
	Owner            common.Address
	Expiry           *big.Int
	Deadline         *big.Int
}
type topupTuple struct {
	Creation        creationTuple
	CreditSource    sourceTuple
	CreditNullifier [32]byte
	PreviousExpiry  *big.Int
}

func TestTopUpNestedTuples(t *testing.T) {
	d, log := fixture(t)
	tx := common.Hash{}
	p := topupTuple{Creation: creationTuple{Sources: []sourceTuple{{big.NewInt(1), [32]byte{1}}}, Nullifiers: [][32]byte{{2}}, CreditCommitment: [32]byte{3}, Expiry: big.NewInt(9), Deadline: big.NewInt(8)}, CreditSource: sourceTuple{big.NewInt(1), [32]byte{4}}, CreditNullifier: [32]byte{5}, PreviousExpiry: big.NewInt(7)}
	ins := log("CommitmentsInserted", 1, tx, []any{big.NewInt(1)}, uint32(4), [][32]byte{{3}})
	top := log("CreditToppedUp", 2, tx, []any{common.Address{}, big.NewInt(1)}, p, []uint32{4}, []delivery{{[]byte{1}, nil}, {[]byte{2}, nil}, {}})
	b, err := d.Decode(model.Block{}, []types.Log{ins, top})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Outputs) != 2 || len(b.Nullifiers) != 2 {
		t.Fatalf("incorrect top-up %+v", b)
	}
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

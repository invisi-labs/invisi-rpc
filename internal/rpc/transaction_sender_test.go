package rpc

import (
	"context"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	"math/big"
	"testing"
)

type senderState struct {
	stateFake
	tx *types.Transaction
}

func (s *senderState) TransactionByHash(context.Context, common.Hash) (*types.Transaction, bool, error) {
	return s.tx, false, nil
}
func TestTransactionSenderRecoveredFromSignature(t *testing.T) {
	key, _ := crypto.GenerateKey()
	to := common.HexToAddress("0x1234")
	for _, legacy := range []bool{false, true} {
		var tx *types.Transaction
		var err error
		if legacy {
			tx, err = types.SignNewTx(key, types.HomesteadSigner{}, &types.LegacyTx{To: &to, Gas: 21000, GasPrice: big.NewInt(1)})
		} else {
			tx, err = types.SignNewTx(key, types.LatestSignerForChainID(big.NewInt(97)), &types.DynamicFeeTx{ChainID: big.NewInt(97), To: &to, Gas: 21000, GasTipCap: big.NewInt(1), GasFeeCap: big.NewInt(2)})
		}
		if err != nil {
			t.Fatal(err)
		}
		api := &API{ChainState: &senderState{tx: tx}, Deployment: model.Deployment{ChainID: "97"}}
		got, err := api.ReadChainState(context.Background(), ChainStateRequest{Kind: "transaction", TransactionHash: tx.Hash()})
		if err != nil || got.TransactionFrom == nil || *got.TransactionFrom != crypto.PubkeyToAddress(key.PublicKey) {
			t.Fatalf("sender missing or incorrect: %+v %v", got, err)
		}
	}
}
func TestTransactionSenderRejectsUnsignedAndWrongChain(t *testing.T) {
	key, _ := crypto.GenerateKey()
	to := common.HexToAddress("0x1234")
	wrong, _ := types.SignNewTx(key, types.LatestSignerForChainID(big.NewInt(98)), &types.DynamicFeeTx{ChainID: big.NewInt(98), To: &to, Gas: 21000, GasTipCap: big.NewInt(1), GasFeeCap: big.NewInt(2)})
	for _, tx := range []*types.Transaction{wrong, types.NewTx(&types.DynamicFeeTx{ChainID: big.NewInt(97), To: &to, Gas: 21000})} {
		api := &API{ChainState: &senderState{tx: tx}, Deployment: model.Deployment{ChainID: "97"}}
		if _, err := api.ReadChainState(context.Background(), ChainStateRequest{Kind: "transaction", TransactionHash: tx.Hash()}); err == nil {
			t.Fatal("invalid signature or chain accepted")
		}
	}
}

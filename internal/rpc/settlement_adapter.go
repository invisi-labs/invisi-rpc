package rpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/invisi-lab/invisi-rpc/internal/model"
	"github.com/invisi-lab/invisi-sdk-go/protocol/settlement"
)

var transferTopic = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))

func decimal(n uint64) string { return strconv.FormatUint(n, 10) }
func operationBase(a *API, b model.Block, hash common.Hash, i int, success bool) settlement.Operation {
	return settlement.Operation{ChainID: a.Deployment.ChainID, ProtocolAddress: a.Deployment.Protocol, BlockNumber: decimal(b.Number), BlockHash: b.Hash, ParentHash: b.ParentHash, Timestamp: decimal(b.Timestamp), TxHash: hash, OperationIndex: fmt.Sprint(i), LogIndex: "0", PublicInputs: []common.Hash{}, NoteNullifiers: []common.Hash{}, CreditNullifiers: []common.Hash{}, NoteOutputs: []settlement.Output{}, CreditOutputs: []settlement.CreditOutput{}, GrantRefs: []settlement.GrantRef{}, Funding: []settlement.FundingReceipt{}, MainCircuitCapacity: "0", WithdrawalAmount: "0", FeeAmount: "0", Success: success}
}
func (a *API) transactionOperationsInBlock(batch *model.Batch, tx *types.Transaction, receipt *types.Receipt) ([]settlement.Operation, error) {
	b := batch.Block
	if receipt == nil || receipt.TxHash != tx.Hash() || receipt.BlockNumber == nil || !receipt.BlockNumber.IsUint64() || receipt.BlockNumber.Uint64() != b.Number || receipt.BlockHash != b.Hash || receipt.Status > 1 {
		return nil, errors.New("inconsistent canonical receipt")
	}
	indexed := []types.Log{}
	for _, l := range batch.Logs {
		if l.Address == a.Deployment.Protocol && l.TxHash == tx.Hash() {
			indexed = append(indexed, l)
		}
	}
	actual := []types.Log{}
	for _, l := range receipt.Logs {
		if l == nil || l.Removed || l.BlockHash != b.Hash || l.TxHash != tx.Hash() || l.BlockNumber != b.Number {
			return nil, errors.New("inconsistent receipt log")
		}
		if l.Address == a.Deployment.Protocol {
			actual = append(actual, *l)
		}
	}
	if !reflect.DeepEqual(indexed, actual) {
		return nil, errors.New("indexed settlement logs disagree with canonical receipt")
	}
	base := operationBase(a, b, tx.Hash(), 0, receipt.Status == 1)
	if tx.To() == nil || *tx.To() != a.Deployment.Protocol {
		base.Kind = "unsupported"
		base.UnsupportedReason = "indirect_call_attribution_requires_traces"
		if len(actual) > 0 {
			base.LogIndex = fmt.Sprint(actual[0].Index)
		}
		return []settlement.Operation{base}, nil
	}
	if len(tx.Data()) > 2<<20 {
		return nil, errors.New("settlement calldata exceeds limit")
	}
	requests, e := settlement.DecodeCalls(tx.Data())
	if e != nil {
		base.Kind = "unsupported"
		base.UnsupportedReason = "unsupported_or_noncanonical_protocol_calldata"
		return []settlement.Operation{base}, nil
	}
	if receipt.Status == 0 {
		out := []settlement.Operation{}
		for i, r := range requests {
			o := operationBase(a, b, tx.Hash(), i, false)
			o.Kind = r.Method
			o.TokenAddress = r.Statement().Token
			o.UnsupportedReason = "reverted_no_settlement"
			if len(requests) > 1 {
				v := fmt.Sprint(i)
				o.MemberIndex = &v
			}
			out = append(out, o)
		}
		return out, nil
	}

	chain, ok := new(big.Int).SetString(a.Deployment.ChainID, 10)
	if !ok {
		return nil, errors.New("invalid chain ID")
	}
	out := make([]settlement.Operation, 0, len(requests))
	usedOutputs := map[common.Hash]bool{}
	expectedFunding := map[string]*big.Int{}
	expectedFees := map[string]*big.Int{}
	add := func(target map[string]*big.Int, token, payer common.Address, amount *big.Int) {
		if amount == nil || amount.Sign() == 0 {
			return
		}
		key := token.Hex() + ":" + payer.Hex()
		if target[key] == nil {
			target[key] = new(big.Int)
		}
		target[key].Add(target[key], amount)
	}
	for i, r := range requests {
		o := operationBase(a, b, tx.Hash(), i, true)
		o.Kind = r.Method
		statement := r.Statement()
		o.TokenAddress = statement.Token
		o.FeeRecipient = statement.FeeRecipient
		o.FeeAmount = statement.FeeAmount.String()
		if len(requests) > 1 {
			v := fmt.Sprint(i)
			o.MemberIndex = &v
		}
		add(expectedFees, o.TokenAddress, o.FeeRecipient, statement.FeeAmount)
		eventName := map[string]string{"shield": "Shield", "createCredit": "CreditCreated", "updateCredit": "CreditUpdated", "revokeCredit": "CreditRevoked", "creditDraw": "CreditDraw"}[r.Method]
		if r.Method == "transact" {
			eventName = "Transact"
			if len(r.Params.Sources) > 1 {
				eventName = "CrossTreeTransact"
			}
			if len(requests) > 1 {
				eventName = "BatchRequest"
			} else {
				decoded, _ := settlement.Decode(tx.Data())
				if len(decoded.Members) > 0 {
					eventName = "BatchRequest"
					v := fmt.Sprint(i)
					o.MemberIndex = &v
				}
			}
		}
		var event *model.Event
		for j := range batch.Events {
			candidate := &batch.Events[j]
			if candidate.TransactionHash != tx.Hash() || candidate.Name != eventName {
				continue
			}
			if eventName == "BatchRequest" {
				var fields map[string]json.RawMessage
				_ = json.Unmarshal(candidate.Data, &fields)
				var index string
				_ = json.Unmarshal(fields["requestIndex"], &index)
				if index != fmt.Sprint(i) {
					continue
				}
			}
			if event != nil {
				return nil, errors.New("ambiguous settlement event attribution")
			}
			event = candidate
		}
		if event == nil {
			return nil, errors.New("successful call has no authenticated settlement event")
		}
		o.LogIndex = fmt.Sprint(event.LogIndex)
		if eventName == "BatchRequest" {
			var v struct {
				Capacity      string      `json:"capacity"`
				StatementHash common.Hash `json:"statementHash"`
			}
			if json.Unmarshal(event.Data, &v) != nil {
				return nil, errors.New("invalid aggregate statement event")
			}
			c, _, _, err := settlement.TransactStatement(chain, a.Deployment.Protocol, &r)
			memberHash, hashErr := settlement.AggregateMemberHash(chain, a.Deployment.Protocol, &r)
			if err != nil || hashErr != nil || v.Capacity != decimal(uint64(c)) || v.StatementHash != memberHash {
				return nil, errors.New("aggregate statement event mismatch")
			}
		}
		var commitments, credits [][32]byte
		switch r.Method {
		case "transact":
			c, w, d, e := settlement.TransactStatement(chain, a.Deployment.Protocol, &r)
			if e != nil {
				o.UnsupportedReason = "invalid_canonical_transact_statement"
			} else {
				o.MainCircuitCapacity = decimal(uint64(c))
				o.OperationDigest = d
				for _, v := range w {
					o.PublicInputs = append(o.PublicInputs, common.Hash(v))
				}
			}
			for _, v := range r.Params.Nullifiers {
				o.NoteNullifiers = append(o.NoteNullifiers, common.Hash(v))
			}
			commitments = r.Params.NoteCommitments
			o.WithdrawalAmount = r.Params.WithdrawalAmount.String()
			if e = matchTransactEvent(*event, r, i, len(requests) > 1 || o.MemberIndex != nil); e != nil {
				return nil, e
			}
		case "shield":
			if len(r.ShieldOutputs) != len(r.Shield.NoteCommitments) {
				return nil, errors.New("shield output shape mismatch")
			}
			commitments = r.Shield.NoteCommitments
		case "createCredit":
			credits = [][32]byte{r.Creation.CreditCommitment}
		case "updateCredit":
			credits = [][32]byte{r.Update.CreditCommitment}
			o.CreditNullifiers = append(o.CreditNullifiers, common.Hash(r.Update.Nullifier))
		case "creditDraw":
			commitments = [][32]byte{r.Draw.NoteCommitment}
			credits = r.Draw.CreditCommitments
			for _, v := range r.Draw.Nullifiers {
				o.CreditNullifiers = append(o.CreditNullifiers, common.Hash(v))
			}
			for _, g := range r.Draw.GrantRefs {
				o.GrantRefs = append(o.GrantRefs, settlement.GrantRef{PayerIndex: g.PayerIndex.String(), Shard: decimal(g.Shard), Slot: decimal(uint64(g.Slot))})
			}
			for _, p := range r.Draw.Payers {
				o.Funding = append(o.Funding, settlement.FundingReceipt{Payer: p.Payer, TokenAddress: o.TokenAddress, Amount: p.Amount.String()})
				add(expectedFunding, o.TokenAddress, p.Payer, p.Amount)
			}
		}
		if r.HasFunding() && r.Funding.Amount.Sign() > 0 {
			o.Funding = append(o.Funding, settlement.FundingReceipt{Payer: r.Funding.Payer, TokenAddress: o.TokenAddress, Amount: r.Funding.Amount.String()})
			add(expectedFunding, o.TokenAddress, r.Funding.Payer, r.Funding.Amount)
		}
		for j, v := range commitments {
			found := false
			for _, output := range batch.Outputs {
				if output.TransactionHash == tx.Hash() && output.Commitment == common.Hash(v) && output.Token == o.TokenAddress {
					if found || usedOutputs[output.Commitment] {
						return nil, errors.New("ambiguous Note output attribution")
					}
					found = true
					usedOutputs[output.Commitment] = true
					n := settlement.Output{NoteCommitment: output.Commitment, TreeID: decimal(output.TreeID), LeafIndex: decimal(uint64(output.LeafIndex))}
					if r.Method == "shield" {
						n.Amount = r.ShieldOutputs[j].Amount.String()
					}
					if r.Method == "creditDraw" {
						n.Amount = new(big.Int).Sub(r.Draw.TotalAmount, r.Draw.FeeAmount).String()
					}
					o.NoteOutputs = append(o.NoteOutputs, n)
				}
			}
			if !found {
				return nil, errors.New("calldata Note output absent from canonical events")
			}
		}
		for _, v := range credits {
			found := false
			for _, output := range batch.CreditStates {
				if output.TransactionHash == tx.Hash() && output.Commitment == common.Hash(v) && output.Token == o.TokenAddress {
					if found || usedOutputs[output.Commitment] {
						return nil, errors.New("ambiguous Credit output attribution")
					}
					found = true
					usedOutputs[output.Commitment] = true
					o.CreditOutputs = append(o.CreditOutputs, settlement.CreditOutput{CreditCommitment: output.Commitment, TreeID: decimal(output.TreeID), LeafIndex: decimal(uint64(output.LeafIndex))})
				}
			}
			if !found {
				return nil, errors.New("calldata Credit output absent from canonical events")
			}
		}
		out = append(out, o)
	}
	// Funding is the gross transfer into Protocol, allocated by actual calldata member amounts.
	// Pooled aggregate pulls may combine members with the same token/payer into one receipt.
	actualFunding := map[string]*big.Int{}
	for _, l := range receipt.Logs {
		if len(l.Topics) == 3 && l.Topics[0] == transferTopic && common.BytesToAddress(l.Topics[2][12:]) == a.Deployment.Protocol {
			if len(l.Data) != 32 || !bytes.Equal(l.Topics[1][:12], make([]byte, 12)) || !bytes.Equal(l.Topics[2][:12], make([]byte, 12)) {
				return nil, errors.New("noncanonical funding transfer receipt")
			}
			payer := common.BytesToAddress(l.Topics[1][12:])
			key := l.Address.Hex() + ":" + payer.Hex()
			if expectedFunding[key] != nil {
				add(actualFunding, l.Address, payer, new(big.Int).SetBytes(l.Data))
			}
		}
	}
	for key, want := range expectedFunding {
		if actualFunding[key] == nil || actualFunding[key].Cmp(want) != 0 {
			for i := range out {
				out[i].UnsupportedReason = "funding_receipts_cannot_be_authenticated"
			}
		}
	}
	actualFees := map[string]*big.Int{}
	for _, event := range batch.Events {
		if event.TransactionHash == tx.Hash() && event.Name == "BroadcasterFee" {
			var v struct {
				Token     common.Address `json:"token"`
				Recipient common.Address `json:"recipient"`
				Amount    string         `json:"amount"`
			}
			if json.Unmarshal(event.Data, &v) != nil {
				return nil, errors.New("invalid fee event")
			}
			n, ok := new(big.Int).SetString(v.Amount, 10)
			if !ok {
				return nil, errors.New("invalid fee event amount")
			}
			add(actualFees, v.Token, v.Recipient, n)
		}
	}
	for key, want := range expectedFees {
		if actualFees[key] == nil || actualFees[key].Cmp(want) != 0 {
			return nil, errors.New("calldata fee disagrees with canonical fee event")
		}
	}
	return out, nil
}
func matchTransactEvent(event model.Event, r settlement.Request, index int, aggregate bool) error {
	var v struct {
		Token             common.Address `json:"token"`
		Nullifiers        []common.Hash  `json:"nullifiers"`
		Commitments       []common.Hash  `json:"noteCommitments"`
		WithdrawalAddress common.Address `json:"withdrawalAddress"`
		WithdrawalAmount  string         `json:"withdrawalAmount"`
		Deadline          string         `json:"deadline"`
		Payer             common.Address `json:"payer"`
		DepositAmount     string         `json:"depositAmount"`
		OutputCount       string         `json:"outputCount"`
		Capacity          string         `json:"capacity"`
	}
	if json.Unmarshal(event.Data, &v) != nil {
		return errors.New("invalid settlement event")
	}
	if v.Token != r.Params.Token || v.WithdrawalAddress != r.Params.WithdrawalAddress || v.WithdrawalAmount != r.Params.WithdrawalAmount.String() || v.Deadline != r.Params.Deadline.String() || len(v.Nullifiers) != len(r.Params.Nullifiers) {
		return errors.New("calldata and settlement event disagree")
	}
	for i, n := range v.Nullifiers {
		if n != common.Hash(r.Params.Nullifiers[i]) {
			return errors.New("calldata nullifier differs from event")
		}
	}
	if aggregate {
		if v.Payer != r.Funding.Payer || v.DepositAmount != r.Funding.Amount.String() || v.OutputCount != fmt.Sprint(len(r.Params.NoteCommitments)) {
			return errors.New("aggregate member event mismatch")
		}
	} else {
		if len(v.Commitments) != len(r.Params.NoteCommitments) {
			return errors.New("calldata output count differs from event")
		}
		for i, c := range v.Commitments {
			if c != common.Hash(r.Params.NoteCommitments[i]) {
				return errors.New("calldata commitment differs from event")
			}
		}
	}
	return nil
}

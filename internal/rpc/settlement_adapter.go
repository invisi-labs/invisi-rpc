package rpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	sdkprotocol "github.com/invisi-labs/invisi-sdk-go/protocol"
	"github.com/invisi-labs/invisi-sdk-go/protocol/settlement"
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
	aggregate, e := matchAggregate(batch, tx.Hash(), tx.Data(), chain, a.Deployment.Protocol, requests)
	if e != nil {
		return nil, e
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
		var event *model.Event
		if r.Method == "transact" || r.Method == "shield" {
			id, err := noteID(chain, a.Deployment.Protocol, r)
			if err != nil {
				return nil, err
			}
			event, err = authenticatedEvent(batch, tx.Hash(), "NoteOperation", map[string]any{"operationId": id})
			if err != nil {
				return nil, err
			}
		} else {
			var err error
			event, err = matchCreditEvents(batch, tx.Hash(), r)
			if err != nil {
				return nil, err
			}
		}
		if event == nil {
			return nil, errors.New("missing canonical settlement event")
		}
		o.LogIndex = fmt.Sprint(event.LogIndex)
		if aggregate {
			v := fmt.Sprint(i)
			o.MemberIndex = &v
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
		case "shield":
			if len(r.ShieldOutputs) != len(r.Shield.NoteCommitments) {
				return nil, errors.New("shield output shape mismatch")
			}
			commitments = r.Shield.NoteCommitments
		case "createCredit":
			credits = [][32]byte{r.Creation.CreditCommitment}
			if r.Creation.HasInitialPayment {
				commitments = [][32]byte{r.Creation.NoteCommitment}
				o.Funding = append(o.Funding, settlement.FundingReceipt{Payer: r.Creation.Owner, TokenAddress: o.TokenAddress, Amount: r.Creation.TotalAmount.String()})
				add(expectedFunding, o.TokenAddress, r.Creation.Owner, r.Creation.TotalAmount)
			}
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
				o.GrantRefs = append(o.GrantRefs, settlement.GrantRef{PayerIndex: g.PayerIndex.String(), GrantID: g.GrantID.String()})
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
					if r.Method == "createCredit" && r.Creation.HasInitialPayment {
						n.Amount = new(big.Int).Sub(r.Creation.TotalAmount, r.Creation.FeeAmount).String()
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

// noteID binds calldata to the unified receipt facts, including ciphertexts.
func noteID(chain *big.Int, core common.Address, r settlement.Request) (common.Hash, error) {
	p := sdkprotocol.CrossTreeTransactParams{Token: r.Statement().Token, WithdrawalAmount: new(big.Int), Deadline: r.Statement().Deadline, FeeRecipient: r.Statement().FeeRecipient, FeeAmount: r.Statement().FeeAmount}
	kind := uint8(1)
	if r.Method == "shield" {
		kind = 0
		p.NoteCommitments = r.Shield.NoteCommitments
	} else {
		p = *abi.ConvertType(r.Params, new(sdkprotocol.CrossTreeTransactParams)).(*sdkprotocol.CrossTreeTransactParams)
	}
	ds := *abi.ConvertType(r.Deliveries, new([]sdkprotocol.Delivery)).(*[]sdkprotocol.Delivery)
	return sdkprotocol.NoteOperationID(sdkprotocol.TransactionContext{ChainID: chain, ProtocolAddress: core}, kind, p, ds, sdkprotocol.FundingParams{Payer: r.Funding.Payer, Amount: r.Funding.Amount})
}
func authenticatedEvent(batch *model.Batch, tx common.Hash, name string, expected map[string]any) (*model.Event, error) {
	var found *model.Event
	wantRaw, e := json.Marshal(receiptJSON(expected))
	if e != nil {
		return nil, e
	}
	var want map[string]any
	if e = json.Unmarshal(wantRaw, &want); e != nil {
		return nil, e
	}
	for i := range batch.Events {
		event := &batch.Events[i]
		if event.TransactionHash != tx || event.Name != name {
			continue
		}
		var got map[string]any
		if e = json.Unmarshal(event.Data, &got); e != nil {
			return nil, e
		}
		matches := true
		for key, value := range want {
			if !reflect.DeepEqual(got[key], value) {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		if found != nil {
			return nil, errors.New("ambiguous settlement event attribution")
		}
		found = event
	}
	if found == nil {
		return nil, fmt.Errorf("missing or mismatched canonical %s event", name)
	}
	return found, nil
}
func matchCreditEvents(batch *model.Batch, tx common.Hash, r settlement.Request) (*model.Event, error) {
	var event *model.Event
	var err error
	switch r.Method {
	case "createCredit":
		p := r.Creation
		if len(r.Deliveries) < 2 {
			return nil, errors.New("invalid creation deliveries")
		}
		creationFee, creationRecipient := p.FeeAmount, p.FeeRecipient
		if p.HasInitialPayment {
			creationFee = new(big.Int)
			creationRecipient = common.Address{}
		}
		event, err = authenticatedEvent(batch, tx, "CreditCreated", map[string]any{"token": p.Token, "owner": p.Owner, "grantId": p.GrantID, "creditCommitment": p.CreditCommitment, "deadline": p.Deadline, "feeRecipient": creationRecipient, "feeAmount": creationFee, "deliveries": r.Deliveries[:2]})
		if err != nil {
			return nil, err
		}
		draws, states := 0, 0
		for _, v := range batch.Events {
			if v.TransactionHash == tx {
				if v.Name == "CreditDraw" {
					draws++
				}
				if v.Name == "CreditStateTransition" {
					states++
				}
			}
		}
		if !p.HasInitialPayment {
			if draws != 0 || states != 0 {
				return nil, errors.New("unexpected creation payment")
			}
			return event, nil
		}
		if len(r.Deliveries) != 3 || draws != 1 || states != 0 {
			return nil, errors.New("invalid initial payment event shape")
		}
		_, err = authenticatedEvent(batch, tx, "CreditDraw", map[string]any{"token": p.Token, "noteCommitment": p.NoteCommitment, "totalAmount": p.TotalAmount, "deadline": p.Deadline, "feeRecipient": p.FeeRecipient, "feeAmount": p.FeeAmount, "delivery": r.Deliveries[2]})
	case "updateCredit":
		event, err = authenticatedEvent(batch, tx, "CreditUpdated", map[string]any{"owner": r.Update.Owner, "params": r.Update, "deliveries": r.Deliveries})
	case "creditDraw":
		p := r.Draw
		n := len(p.CreditCommitments)
		if len(r.Deliveries) != 2*n+1 {
			return nil, errors.New("invalid Credit draw deliveries")
		}
		event, err = authenticatedEvent(batch, tx, "CreditDraw", map[string]any{"token": p.Token, "noteCommitment": p.NoteCommitment, "totalAmount": p.TotalAmount, "deadline": p.Deadline, "feeRecipient": p.FeeRecipient, "feeAmount": p.FeeAmount, "delivery": r.Deliveries[2*n]})
		if err != nil {
			return nil, err
		}
		_, err = authenticatedEvent(batch, tx, "CreditStateTransition", map[string]any{"noteCommitment": p.NoteCommitment, "state": map[string]any{"sources": p.Sources, "payers": p.Payers, "grantRefs": p.GrantRefs, "nullifiers": p.Nullifiers, "creditCommitments": p.CreditCommitments, "recoveryAmounts": p.RecoveryAmounts}, "creditDeliveries": r.Deliveries[:2*n]})
	case "revokeCredit":
		p := r.Revocation
		event, err = authenticatedEvent(batch, tx, "CreditRevoked", map[string]any{"owner": p.Owner, "token": p.Token, "grantId": p.GrantID, "feeRecipient": p.FeeRecipient, "feeAmount": p.FeeAmount})
	}
	return event, err
}
func matchAggregate(batch *model.Batch, tx common.Hash, data []byte, chain *big.Int, core common.Address, requests []settlement.Request) (bool, error) {
	decoded, err := settlement.Decode(data)
	if err != nil {
		return false, err
	}
	aggregate := len(decoded.Members) > 0
	count := 0
	for _, event := range batch.Events {
		if event.TransactionHash == tx && event.Name == "TransactAggregated" {
			count++
		}
	}
	if !aggregate {
		if count != 0 {
			return false, errors.New("unexpected aggregate receipt")
		}
		return false, nil
	}
	ids := []common.Hash{}
	for _, r := range requests {
		id, e := noteID(chain, core, r)
		if e != nil {
			return false, e
		}
		ids = append(ids, id)
	}
	if count != 1 {
		return false, errors.New("missing or duplicate aggregate receipt")
	}
	_, err = authenticatedEvent(batch, tx, "TransactAggregated", map[string]any{"proofHash": crypto.Keccak256Hash(decoded.Proof), "operationIds": ids})
	return true, err
}

// receiptJSON keeps EVM integers lossless for JavaScript consumers and bytes in hex.
func receiptJSON(value any) any {
	switch v := value.(type) {
	case uint64:
		return strconv.FormatUint(v, 10)
	case *big.Int:
		return v.String()
	case common.Address:
		return v.Hex()
	case common.Hash:
		return v.Hex()
	case [32]byte:
		return common.Hash(v).Hex()
	case []byte:
		return hexutil.Encode(v)
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			out[k] = receiptJSON(item)
		}
		return out
	}
	v := reflect.ValueOf(value)
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.Slice, reflect.Array:
		out := make([]any, v.Len())
		for i := range out {
			out[i] = receiptJSON(v.Index(i).Interface())
		}
		return out
	case reflect.Struct:
		out := make(map[string]any, v.NumField())
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i)
			if !field.IsExported() {
				continue
			}
			name := field.Name
			name = strings.ToLower(name[:1]) + name[1:]
			out[name] = receiptJSON(v.Field(i).Interface())
		}
		return out
	}
	return value
}

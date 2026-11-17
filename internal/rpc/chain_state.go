package rpc

import (
	"bytes"
	"context"
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	gethrpc "github.com/ethereum/go-ethereum/rpc"
	"github.com/invisi-lab/invisi-rpc/internal/model"
	"github.com/invisi-lab/invisi-sdk-go/protocol/contracts"
)

// ChainStateReader deliberately has no submission or generic RPC method.
type ChainStateReader interface {
	HeaderByNumber(context.Context, *big.Int) (*types.Header, error)
	CodeAt(context.Context, common.Address, *big.Int) ([]byte, error)
	CallContract(context.Context, ethereum.CallMsg, *big.Int) ([]byte, error)
	TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error)
}
type ChainStateRequest struct {
	Kind            string         `json:"kind"`
	Block           string         `json:"block,omitempty"`
	Address         common.Address `json:"address,omitempty"`
	From            common.Address `json:"from,omitempty"`
	Data            hexutil.Bytes  `json:"data,omitempty"`
	Gas             uint64         `json:"gas,omitempty"`
	TransactionHash common.Hash    `json:"transactionHash,omitempty"`
}
type ChainStateResponse struct {
	VersionInfo
	Deployment  model.Deployment   `json:"deployment"`
	Header      *types.Header      `json:"header,omitempty"`
	HeaderHash  common.Hash        `json:"headerHash,omitempty"`
	Data        hexutil.Bytes      `json:"data"`
	Receipt     *types.Receipt     `json:"receipt"`
	Transaction *types.Transaction `json:"transaction,omitempty"`
	Pending     bool               `json:"pending"`
}

func chainBlock(s string) (*big.Int, error) {
	switch s {
	case "", "latest":
		return nil, nil
	case "safe":
		return big.NewInt(int64(gethrpc.SafeBlockNumber)), nil
	case "finalized":
		return big.NewInt(int64(gethrpc.FinalizedBlockNumber)), nil
	}
	n, e := hexutil.DecodeBig(s)
	if e != nil || !n.IsUint64() || n.BitLen() > 63 {
		return nil, errors.New("invalid chain state block")
	}
	return n, nil
}
func selector(data []byte, sig string, n int) bool {
	return len(data) == 4+32*n && bytes.Equal(data[:4], crypto.Keccak256([]byte(sig))[:4])
}
func (a *API) allowedCall(r ChainStateRequest) bool {
	if len(r.Data) < 4 || len(r.Data) > 128<<10 || r.Address == (common.Address{}) {
		return false
	}
	if r.Address == a.Deployment.Protocol {
		for sig, n := range map[string]int{"VERSION()": 0, "addressRegistry()": 0, "rootHistory(uint256,bytes32)": 2, "isNullifierSpent(bytes32)": 1, "nonces(address)": 1} {
			if selector(r.Data, sig, n) {
				return true
			}
		}
		ab, _ := contracts.InvisiCoreMetaData.GetAbi()
		m, e := ab.MethodById(r.Data[:4])
		if e != nil {
			return false
		}
		switch m.RawName {
		case "transact", "crossTreeTransact", "shield":
		default:
			return false
		}
		values, e := m.Inputs.Unpack(r.Data[4:])
		if e != nil {
			return false
		}
		canonical, e := m.Inputs.Pack(values...)
		return e == nil && bytes.Equal(canonical, r.Data[4:])
	}
	if a.Deployment.Registry != nil && r.Address == a.Deployment.Registry.Address {
		for sig, n := range map[string]int{"isRegistered(address)": 1, "nonceOf(address)": 1, "receivingKeysOf(address)": 1, "CHAIN_ID()": 0} {
			if selector(r.Data, sig, n) {
				return true
			}
		}
		return false
	}
	if a.Deployment.BroadcasterRegistry != nil && r.Address == a.Deployment.BroadcasterRegistry.Address {
		for sig, n := range map[string]int{"broadcasterOf(address)": 1} {
			if selector(r.Data, sig, n) {
				return true
			}
		}
		return false
	}
	// ERC20 public reads only; arbitrary token targets never receive transaction calldata.
	for sig, n := range map[string]int{"name()": 0, "symbol()": 0, "decimals()": 0, "balanceOf(address)": 1, "allowance(address,address)": 2} {
		if selector(r.Data, sig, n) {
			return true
		}
	}
	return false
}
func (a *API) ReadChainState(ctx context.Context, r ChainStateRequest) (ChainStateResponse, error) {
	out := ChainStateResponse{VersionInfo: a.version, Deployment: a.Deployment}
	bad := errors.New("invalid or disallowed chain state request")
	if a.ChainState == nil {
		return out, errors.New("chain state unavailable")
	}
	n, e := chainBlock(r.Block)
	if e != nil {
		return out, e
	}
	if r.Gas > 30_000_000 || len(r.Data) > 128<<10 {
		return out, bad
	}
	switch r.Kind {
	case "header":
		out.Header, e = a.ChainState.HeaderByNumber(ctx, n)
		if e == nil && out.Header != nil {
			if out.Header.Number == nil || !out.Header.Number.IsUint64() || (n != nil && n.Sign() >= 0 && out.Header.Number.Cmp(n) != 0) {
				return out, errors.New("upstream header mismatch")
			}
			out.HeaderHash = out.Header.Hash()
		}
	case "code":
		if r.Address == (common.Address{}) {
			return out, bad
		}
		out.Data, e = a.ChainState.CodeAt(ctx, r.Address, n)
	case "call":
		if !a.allowedCall(r) {
			return out, bad
		}
		gas := r.Gas
		if gas == 0 {
			gas = 30_000_000
		}
		out.Data, e = a.ChainState.CallContract(ctx, ethereum.CallMsg{To: &r.Address, From: r.From, Data: r.Data, Gas: gas}, n)
	case "transaction":
		reader, ok := a.ChainState.(interface {
			TransactionByHash(context.Context, common.Hash) (*types.Transaction, bool, error)
		})
		if !ok || r.TransactionHash == (common.Hash{}) {
			return out, bad
		}
		out.Transaction, out.Pending, e = reader.TransactionByHash(ctx, r.TransactionHash)
		if errors.Is(e, ethereum.NotFound) {
			e = nil
		}
		if e == nil && out.Transaction != nil && out.Transaction.Hash() != r.TransactionHash {
			return out, errors.New("upstream transaction mismatch")
		}
	case "receipt":
		if r.TransactionHash == (common.Hash{}) {
			return out, bad
		}
		out.Receipt, e = a.ChainState.TransactionReceipt(ctx, r.TransactionHash)
		if errors.Is(e, ethereum.NotFound) {
			e = nil
			out.Receipt = nil
		}
		if e == nil && out.Receipt != nil {
			v := out.Receipt
			if v.TxHash != r.TransactionHash || v.BlockNumber == nil || !v.BlockNumber.IsUint64() {
				return out, errors.New("upstream receipt mismatch")
			}
			h, err := a.ChainState.HeaderByNumber(ctx, v.BlockNumber)
			if err != nil {
				return out, err
			}
			if h == nil || h.Number == nil || h.Number.Cmp(v.BlockNumber) != 0 || h.Hash() != v.BlockHash {
				return out, errors.New("noncanonical receipt")
			}
		}
	default:
		return out, bad
	}
	if len(out.Data) > 1<<20 {
		return ChainStateResponse{}, errors.New("chain state response too large")
	}
	if e == nil {
		e = checkResponseSize(out)
	}
	return out, e
}

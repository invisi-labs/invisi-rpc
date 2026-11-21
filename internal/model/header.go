package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// DecodeCompleteHeader rejects omitted zero-valued RLP fields. Geth can decode
// some such omissions into the same hash, so hash equality alone is insufficient.
func DecodeCompleteHeader(raw []byte) (*types.Header, error) {
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(raw, &fields); e != nil {
		return nil, e
	}
	present := func(k string) bool {
		v := fields[k]
		return len(v) > 0 && !bytes.Equal(bytes.TrimSpace(v), []byte("null"))
	}
	for _, k := range []string{"parentHash", "sha3Uncles", "miner", "stateRoot", "transactionsRoot", "receiptsRoot", "logsBloom", "difficulty", "number", "gasLimit", "gasUsed", "timestamp", "extraData", "mixHash", "nonce"} {
		if !present(k) {
			return nil, errors.New("incomplete original header")
		}
	}
	optional := []string{"baseFeePerGas", "withdrawalsRoot", "blobGasUsed", "excessBlobGas", "parentBeaconBlockRoot", "requestsHash", "slotNumber"}
	seen := false
	for i := len(optional) - 1; i >= 0; i-- {
		if present(optional[i]) {
			seen = true
		} else if seen {
			return nil, errors.New("incomplete original header fork fields")
		}
	}
	var h types.Header
	if e := json.Unmarshal(raw, &h); e != nil {
		return nil, e
	}
	var declared struct {
		Hash *common.Hash `json:"hash"`
	}
	if e := json.Unmarshal(raw, &declared); e != nil {
		return nil, e
	}
	if declared.Hash == nil || h.Number == nil || !h.Number.IsUint64() || h.Number.BitLen() > 63 || *declared.Hash != h.Hash() {
		return nil, errors.New("invalid original header hash")
	}
	return &h, nil
}

// BlockHeader preserves the original hashable header, not a reconstructed Block.
type BlockHeader struct {
	Header     *types.Header `json:"header"`
	HeaderHash common.Hash   `json:"headerHash"`
}

// ValidateHeader permits legacy batches without complete headers. A present
// header must bind every normalized block field before storage or snapshot use.
func (b Batch) ValidateHeader() error {
	h := b.Header
	if h == nil {
		return nil
	}
	if h.Difficulty == nil {
		return errors.New("incomplete original header difficulty")
	}
	later := false
	optional := []bool{h.BaseFee != nil, h.WithdrawalsHash != nil, h.BlobGasUsed != nil, h.ExcessBlobGas != nil, h.ParentBeaconRoot != nil, h.RequestsHash != nil, h.SlotNumber != nil}
	for i := len(optional) - 1; i >= 0; i-- {
		if optional[i] {
			later = true
		} else if later {
			return errors.New("incomplete original header fork fields")
		}
	}
	if h.Number == nil || !h.Number.IsUint64() || h.Number.Uint64() > math.MaxInt64 || h.Number.Uint64() != b.Block.Number || h.Hash() != b.Block.Hash || h.ParentHash != b.Block.ParentHash || h.Time != b.Block.Timestamp {
		return errors.New("complete header differs from indexed block")
	}
	return nil
}

package protocol

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

type versionCaller struct {
	result  []byte
	err     error
	message ethereum.CallMsg
	block   *big.Int
}

func (c *versionCaller) CallContract(_ context.Context, msg ethereum.CallMsg, block *big.Int) ([]byte, error) {
	c.message = msg
	c.block = block
	return c.result, c.err
}
func TestReadVersion(t *testing.T) {
	addr := common.HexToAddress("0x1234")
	for _, tc := range []struct {
		name  string
		bytes []byte
		err   error
		want  string
	}{
		{"one", common.LeftPadBytes([]byte{1}, 32), nil, "1"},
		{"future", common.LeftPadBytes([]byte{2}, 32), nil, "2"},
		{"zero", make([]byte, 32), nil, ""},
		{"empty", nil, nil, ""},
		{"truncated", []byte{1}, nil, ""},
		{"rpc", nil, errors.New("unavailable"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &versionCaller{result: tc.bytes, err: tc.err}
			got, err := ReadVersion(context.Background(), c, addr)
			if got != tc.want || (err == nil) != (tc.want != "") {
				t.Fatalf("got %q %v", got, err)
			}
			if c.message.To == nil || *c.message.To != addr || string(c.message.Data) != string(crypto.Keccak256([]byte("VERSION()"))[:4]) || c.block != nil {
				t.Fatal("incorrect VERSION eth_call")
			}
		})
	}
}

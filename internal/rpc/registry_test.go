package rpc

import (
	"context"
	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
	"strings"
	"testing"
)

type registryBackend struct {
	fakeBackend
	registration *model.RegisteredReceivingAddress
}

func (f *registryBackend) ReceivingAddress(context.Context, common.Address) (*model.RegisteredReceivingAddress, error) {
	return f.registration, nil
}
func TestReceivingAddressWire(t *testing.T) {
	f := &registryBackend{registration: &model.RegisteredReceivingAddress{WalletIdentity: common.HexToAddress("0x1234"), Nonce: 18446744073709551615, Block: model.Block{Number: 15}}}
	a, err := New(f, model.Deployment{ChainID: "97", Registry: &model.ContractDeployment{Address: common.HexToAddress("0x5678"), RuntimeCodeHash: common.HexToHash("0x1234")}}, 24, false, "1")
	if err != nil {
		t.Fatal(err)
	}
	h, err := HTTPHandler(a)
	if err != nil {
		t.Fatal(err)
	}
	r := call(t, h, "invisi_getReceivingAddress", []any{common.HexToAddress("0x1234").Hex()}, "")
	if r.Error != nil || !strings.Contains(string(r.Result), `"nonce":"18446744073709551615"`) || !strings.Contains(string(r.Result), `"ownerAddress":"0x0000000000000000000000000000000000001234"`) || strings.Contains(string(r.Result), `"walletIdentity"`) {
		t.Fatalf("unexpected response: %+v", r)
	}
	f.registration = nil
	r = call(t, h, "invisi_getReceivingAddress", []any{common.HexToAddress("0x1234").Hex()}, "")
	if r.Error != nil || string(r.Result) != "null" {
		t.Fatalf("unexpected unregistered response: %+v", r)
	}
	for _, owner := range []string{"0x1234", common.Address{}.Hex()} {
		if r := call(t, h, "invisi_getReceivingAddress", []any{owner}, ""); r.Error == nil {
			t.Fatal("expected invalid owner rejection")
		}
	}
	a.Deployment.Registry = nil
	if _, err := a.GetReceivingAddress(context.Background(), common.HexToAddress("0x1234")); err == nil {
		t.Fatal("expected missing registry rejection")
	}
}

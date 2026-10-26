package protocol

import (
	"encoding/json"
	"math/big"
	"testing"
)

func TestEventJSONLossless(t *testing.T) {
	n := new(big.Int).Lsh(big.NewInt(1), 128)
	value := map[string]any{"amount": n, "nested": struct {
		Amount     *big.Int
		Ciphertext []byte
	}{n, []byte{1, 2}}}
	raw, err := json.Marshal(eventJSON(value))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["amount"] != n.String() {
		t.Fatalf("amount lost precision: %s", raw)
	}
	nested := decoded["nested"].(map[string]any)
	if nested["amount"] != n.String() || nested["ciphertext"] != "0x0102" {
		t.Fatalf("nested ABI values not normalized: %s", raw)
	}
}

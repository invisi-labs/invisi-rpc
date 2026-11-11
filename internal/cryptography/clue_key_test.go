package cryptography

import (
	"encoding/json"
	"github.com/ethereum/go-ethereum/common"
	"os"
	"testing"
)

func TestFMDCanonicalRustPointCorpus(t *testing.T) {
	raw, e := os.ReadFile("testdata/decaf377-clue-keys.json")
	if e != nil {
		t.Fatal(e)
	}
	var corpus []struct {
		Key   string
		Valid bool
	}
	if e = json.Unmarshal(raw, &corpus); e != nil {
		t.Fatal(e)
	}
	if len(corpus) != 322 {
		t.Fatal("unexpected independent corpus")
	}
	for _, v := range corpus {
		if e := ValidateClueKey(common.FromHex(v.Key)); (e == nil) != v.Valid {
			t.Fatalf("point %s valid%v: %v", v.Key, v.Valid, e)
		}
	}
}

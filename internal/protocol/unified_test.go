package protocol

import (
	"encoding/json"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-labs/invisi-rpc/internal/model"
	"os"
	"testing"
)

func TestUnifiedReceiptFixtures(t *testing.T) {
	raw, e := os.ReadFile("testdata/unified-receipts.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct {
		Cases []struct {
			Name     string
			Context  struct{ ProtocolAddress common.Address }
			Receipt  struct{ Logs []types.Log }
			Expected struct{ OrdinaryNotes, CreditStates, Nullifiers int }
		}
	}
	if e = json.Unmarshal(raw, &f); e != nil {
		t.Fatal(e)
	}
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			d, e := NewDecoder()
			if e != nil {
				t.Fatal(e)
			}
			logs := []types.Log{}
			for _, l := range c.Receipt.Logs {
				if l.Address == c.Context.ProtocolAddress {
					logs = append(logs, l)
				}
			}
			b, e := d.Decode(model.Block{}, logs)
			if e != nil {
				t.Fatal(e)
			}
			if len(b.Outputs) != c.Expected.OrdinaryNotes || len(b.CreditStates) != c.Expected.CreditStates || len(b.Nullifiers)+len(b.CreditNullifiers) != c.Expected.Nullifiers {
				t.Fatalf("notes=%d credits=%d nullifiers=%d", len(b.Outputs), len(b.CreditStates), len(b.Nullifiers)+len(b.CreditNullifiers))
			}
		})
	}
}

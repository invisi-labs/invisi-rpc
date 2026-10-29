package model

import (
	"encoding/json"
	"testing"
)

func TestBlockRequiresExplicitUint64Timestamp(t *testing.T) {
	for _, data := range []string{`{"number":1}`, `{"number":1,"timestamp":null}`, `{"number":1,"timestamp":-1}`, `{"number":1,"timestamp":18446744073709551616}`} {
		var b Block
		if err := json.Unmarshal([]byte(data), &b); err == nil {
			t.Fatalf("accepted invalid block timestamp: %s", data)
		}
	}
	for _, data := range []string{`{"number":1,"timestamp":0}`, `{"number":1,"timestamp":18446744073709551615}`} {
		var b Block
		if err := json.Unmarshal([]byte(data), &b); err != nil {
			t.Fatalf("valid uint64 timestamp: %v", err)
		}
	}
}

func TestBatchRequiresCurrentBlockRecord(t *testing.T) {
	for _, data := range []string{`{}`, `{"block":null}`, `{"block":{"number":0}}`} {
		var b Batch
		if err := json.Unmarshal([]byte(data), &b); err == nil {
			t.Fatalf("accepted missing block/time: %s", data)
		}
	}
}

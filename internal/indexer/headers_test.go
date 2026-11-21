package indexer

import (
	"context"
	"testing"
)

func TestSequentialIndexerRetainsCompleteHeader(t *testing.T) {
	i, c, s, _ := fixture()
	c.head = 2
	if _, e := i.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
	if len(s.applied) != 1 || s.applied[0].Header == nil || s.applied[0].Header.Hash() != c.headers[2].Hash() {
		t.Fatal("indexer discarded original header")
	}
}

func TestRangeIndexerRetainsCompleteHeaders(t *testing.T) {
	i, c, s, _ := rangeFixture()
	if _, e := i.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
	for _, b := range s.applied {
		if b.Header == nil || b.Header.Hash() != c.headers[b.Block.Number].Hash() {
			t.Fatal("range discarded original header")
		}
	}
	if len(s.applied) == 0 {
		t.Fatal("no range applied")
	}
}

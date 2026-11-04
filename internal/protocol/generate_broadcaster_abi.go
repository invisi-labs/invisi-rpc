//go:build ignore

// Generate the event-only ABI from the sibling Protocol's compiled artifact.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func main() {
	source := filepath.Join("..", "..", "..", "invisi-protocol", "contracts", "out", "BroadcasterRegistry.sol", "BroadcasterRegistry.json")
	b, e := os.ReadFile(source)
	if e != nil {
		panic(e)
	}
	var artifact struct {
		ABI []json.RawMessage `json:"abi"`
	}
	if e = json.Unmarshal(b, &artifact); e != nil {
		panic(e)
	}
	events := []json.RawMessage{}
	for _, raw := range artifact.ABI {
		var item struct {
			Type string `json:"type"`
		}
		if e = json.Unmarshal(raw, &item); e != nil {
			panic(e)
		}
		if item.Type == "event" {
			events = append(events, raw)
		}
	}
	b, e = json.MarshalIndent(events, "", "  ")
	if e != nil {
		panic(e)
	}
	if e = os.WriteFile("BroadcasterRegistry.abi", append(b, '\n'), 0644); e != nil {
		panic(e)
	}
}

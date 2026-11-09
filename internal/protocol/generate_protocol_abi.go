//go:build ignore

// Generate the event ABI only after authenticating the artifact against local Solidity sources.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ethereum/go-ethereum/crypto"
)

func main() {
	root := filepath.Join("..", "..", "..", "invisi-protocol")
	source := filepath.Join(root, "contracts", "out", "InvisiProtocol.sol", "InvisiProtocol.json")
	raw, e := os.ReadFile(source)
	must(e)
	var artifact struct {
		ABI      []json.RawMessage `json:"abi"`
		Metadata struct {
			Sources map[string]struct {
				Keccak string `json:"keccak256"`
			} `json:"sources"`
		} `json:"metadata"`
	}
	must(json.Unmarshal(raw, &artifact))
	if len(artifact.Metadata.Sources) == 0 {
		panic("artifact source metadata missing")
	}
	for path, record := range artifact.Metadata.Sources {
		b, e := os.ReadFile(filepath.Join(root, path))
		must(e)
		if crypto.Keccak256Hash(b).Hex() != record.Keccak {
			panic("artifact does not match current source: " + path)
		}
	}
	events := []json.RawMessage{}
	for _, entry := range artifact.ABI {
		var item struct {
			Type string `json:"type"`
		}
		must(json.Unmarshal(entry, &item))
		if item.Type == "event" {
			events = append(events, entry)
		}
	}
	encoded, e := json.MarshalIndent(events, "", "  ")
	must(e)
	must(os.WriteFile("InvisiProtocol.abi", append(encoded, '\n'), 0644))
	shield := []json.RawMessage{}
	for _, entry := range artifact.ABI {
		var item struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		must(json.Unmarshal(entry, &item))
		if item.Type == "function" && item.Name == "shield" {
			shield = append(shield, entry)
		}
	}
	if len(shield) != 1 {
		panic("canonical shield function missing or ambiguous")
	}
	encoded, e = json.MarshalIndent(shield, "", "  ")
	must(e)
	fixtureDirectory := filepath.Join("..", "integration", "testdata")
	must(os.MkdirAll(fixtureDirectory, 0755))
	must(os.WriteFile(filepath.Join(fixtureDirectory, "shield.abi.json"), append(encoded, '\n'), 0644))
	head, e := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	must(e)
	sourceStatus, e := exec.Command("git", "-C", root, "status", "--porcelain", "--", "contracts/src").Output()
	must(e)
	sum := sha256.Sum256(raw)
	provenance := struct {
		ProtocolCommit  string `json:"protocolCommit"`
		ArtifactSHA256  string `json:"artifactSha256"`
		VerifiedSources int    `json:"verifiedSources"`
		SourceTreeClean bool   `json:"sourceTreeClean"`
	}{string(head[:len(head)-1]), hex.EncodeToString(sum[:]), len(artifact.Metadata.Sources), len(sourceStatus) == 0}
	encoded, e = json.MarshalIndent(provenance, "", "  ")
	must(e)
	must(os.WriteFile("protocol-source.json", append(encoded, '\n'), 0644))
	fmt.Println("Verified artifact source closure and generated event ABI")
}
func must(e error) {
	if e != nil {
		panic(e)
	}
}

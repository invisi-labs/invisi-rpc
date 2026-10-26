// Package deployment loads Protocol deployments from a single JSON file.
package deployment

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-protocol-rpc/internal/model"
)

const DefaultFile = "deployment.json"

type Profile struct {
	Name            string         `json:"name,omitempty"`
	ProtocolAddress common.Address `json:"protocolAddress"`
	RuntimeCodeHash common.Hash    `json:"runtimeCodeHash"`
	FromBlock       uint64         `json:"fromBlock"`
}
type Catalog struct {
	Version int                `json:"version"`
	Chains  map[string]Profile `json:"chains"`
}

func canonicalID(id string) bool {
	n, ok := new(big.Int).SetString(id, 10)
	return ok && n.Sign() > 0 && n.String() == id
}

// Load reads the selected file, defaulting to deployment.json in the current directory.
// Missing or invalid files are errors; there is no fallback catalog.
func Load(path string) (Catalog, error) {
	if path == "" {
		path = DefaultFile
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Catalog{}, fmt.Errorf("read deployment file %s: %w", path, err)
	}
	if err := uniqueJSONKeys(data); err != nil {
		return Catalog{}, err
	}
	var c Catalog
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return Catalog{}, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return Catalog{}, errors.New("catalog must contain exactly one JSON object")
	}
	if c.Version != 1 {
		return Catalog{}, fmt.Errorf("unsupported deployment catalog version %d", c.Version)
	}
	if len(c.Chains) == 0 {
		return Catalog{}, errors.New("deployment catalog requires at least one chain")
	}
	for id, p := range c.Chains {
		if !canonicalID(id) {
			return Catalog{}, fmt.Errorf("chain ID %q must be canonical positive decimal", id)
		}
		if err := validateProfile(p); err != nil {
			return Catalog{}, fmt.Errorf("chain %s: %w", id, err)
		}
	}
	return c, nil
}
func validateProfile(p Profile) error {
	if p.ProtocolAddress == (common.Address{}) || p.RuntimeCodeHash == (common.Hash{}) {
		return errors.New("nonzero protocolAddress and runtimeCodeHash are required")
	}
	if p.FromBlock > math.MaxInt64 {
		return errors.New("fromBlock exceeds database range")
	}
	return nil
}
func (c Catalog) Resolve(chainID string) (model.Deployment, error) {
	if c.Version != 1 || !canonicalID(chainID) {
		return model.Deployment{}, errors.New("invalid catalog version or chain ID")
	}
	p, ok := c.Chains[chainID]
	if !ok {
		return model.Deployment{}, fmt.Errorf("no deployment profile for chain %s", chainID)
	}
	if err := validateProfile(p); err != nil {
		return model.Deployment{}, err
	}
	return model.Deployment{ChainID: chainID, Protocol: p.ProtocolAddress, RuntimeCodeHash: p.RuntimeCodeHash, FromBlock: p.FromBlock}, nil
}

// Reject duplicate keys before typed decoding; encoding/json otherwise silently
// accepts the last value, which can obscure the deployment a user intended.
func uniqueJSONKeys(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	var value func() error
	value = func() error {
		t, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, e := d.Token()
				if e != nil {
					return e
				}
				name, ok := key.(string)
				if !ok {
					return errors.New("invalid JSON object key")
				}
				if seen[name] {
					return fmt.Errorf("duplicate JSON key %q", name)
				}
				seen[name] = true
				if e = value(); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e := value(); e != nil {
					return e
				}
			}
		default:
			return errors.New("unexpected JSON delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := value(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("catalog must contain exactly one JSON object")
	}
	return nil
}

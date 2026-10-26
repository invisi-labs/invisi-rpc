package cryptography

// Ported from invisi-sdk-go/core, wallet-transition research snapshot (2026-10-23),
// revision 1b78d5929ac79de83bc99a70bcfb9b904ce2c1d3.
// This standalone copy has no SDK dependency; independent fixtures preserve compatibility.

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

func HexToElement(h string) (fr.Element, error) {
	var e fr.Element
	h = strings.TrimPrefix(h, "0x")
	h = strings.TrimPrefix(h, "0X")
	if len(h)%2 != 0 {
		h = "0" + h
	}
	b, err := hex.DecodeString(h)
	if err != nil {
		return e, fmt.Errorf("invalid hex: %w", err)
	}
	// A canonical BN254 element fits in 32 bytes; a longer decode makes the
	// left-pad offset (32-len(b)) negative and panics the copy below.
	if len(b) > 32 {
		return e, fmt.Errorf("value out of BN254 field range: %d bytes exceeds 32", len(b))
	}
	padded := make([]byte, 32)
	copy(padded[32-len(b):], b)
	if err := e.SetBytesCanonical(padded); err != nil {
		return e, fmt.Errorf("value out of BN254 field range: %w", err)
	}
	return e, nil
}

func ElementToHex(e fr.Element) string {
	b := e.Bytes()
	return "0x" + hex.EncodeToString(b[:])
}

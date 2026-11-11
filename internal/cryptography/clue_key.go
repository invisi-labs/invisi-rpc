package cryptography

import "fmt"

func ValidateClueKey(key []byte) error {
	p, e := decafDecode(key)
	if e != nil {
		return e
	}
	if p.X.IsZero() {
		return fmt.Errorf("clue key must not be identity")
	}
	return nil
}

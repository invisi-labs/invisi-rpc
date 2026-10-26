package cryptography

import (
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/common"
	"testing"
)

// Frozen independent expected values from contracts/src/libraries/IncrementalMerkleTree.sol.
func TestSolidityEmptyNodes(t *testing.T) {
	expected := []string{"0x0000000000000000000000000000000000000000000000000000000000000000", "0x0b63a53787021a4a962a452c2921b3663aff1ffd8d5510540f8e659e782956f1", "0x0e34ac2c09f45a503d2908bcb12f1cbae5fa4065759c88d501c097506a8b2290", "0x21f9172d72fdcdafc312eee05cf5092980dda821da5b760a9fb8dbdf607c8a20", "0x2373ea368857ec7af97e7b470d705848e2bf93ed7bef142a490f2119bcf82d8e", "0x120157cfaaa49ce3da30f8b47879114977c24b266d58b0ac18b325d878aafddf", "0x01c28fe1059ae0237b72334700697bdf465e03df03986fe05200cadeda66bd76", "0x2d78ed82f93b61ba718b17c2dfe5b52375b4d37cbbed6f1fc98b47614b0cf21b", "0x067243231eddf4222f3911defbba7705aff06ed45960b27f6f91319196ef97e1", "0x1849b85f3c693693e732dfc4577217acc18295193bede09ce8b97ad910310972", "0x2a775ea761d20435b31fa2c33ff07663e24542ffb9e7b293dfce3042eb104686", "0x0f320b0703439a8114f81593de99cd0b8f3b9bf854601abb5b2ea0e8a3dda4a7", "0x0d07f6e7a8a0e9199d6d92801fff867002ff5b4808962f9da2ba5ce1bdd26a73", "0x1c4954081e324939350febc2b918a293ebcdaead01be95ec02fcbe8d2c1635d1", "0x0197f2171ef99c2d053ee1fb5ff5ab288d56b9b41b4716c9214a4d97facc4c4a", "0x2b9cdd484c5ba1e4d6efcc3f18734b5ac4c4a0b9102e2aeb48521a661d3feee9", "0x14f44d672eb357739e42463497f9fdac46623af863eea4d947ca00a497dcdeb3"}
	for i, want := range expected {
		if EmptyNodes[i] != common.HexToHash(want) {
			t.Fatalf("level %d: %s", i, EmptyNodes[i])
		}
	}
}
func TestHashPairCanonical(t *testing.T) {
	bad := common.BigToHash(fr.Modulus())
	for _, pair := range [][2]common.Hash{{bad, {}}, {{}, bad}} {
		if _, err := HashPair(pair[0], pair[1]); err == nil {
			t.Fatal("modulus accepted")
		}
	}
	got, err := HashPair(common.HexToHash("0x01"), common.HexToHash("0x02"))
	if err != nil {
		t.Fatal(err)
	}
	if got != common.HexToHash("0x038682aa1cb5ae4e0a3f13da432a95c77c5c111f6f030faf9cad641ce1ed7383") {
		t.Fatal(got)
	}
}

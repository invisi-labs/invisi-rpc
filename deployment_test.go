package deployment

import (
	"errors"
	"os"
	"testing"
)

func TestLoadDefaultFileOnly(t *testing.T) {
	raw, err := os.ReadFile("testdata/deployment-fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	if _, err = Load(""); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file must fail without fallback: %v", err)
	}
	if err = os.WriteFile("deployment.json", raw, 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.Resolve("31337"); err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.Resolve("42161"); err == nil {
		t.Fatal("unlisted chain accepted")
	}
	if err = os.WriteFile("deployment.json", []byte(`{"version":1,"chains":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Load(""); err == nil {
		t.Fatal("empty file must not fall back")
	}
}

package fixtures

import (
	"strings"
	"testing"
)

func TestEmbedContainsDotfiles(t *testing.T) {
	seed := DemoSeed()
	if _, ok := seed.Files[".ants/capabilities.yaml"]; !ok {
		t.Fatalf("capabilities.yaml missing from seed: %v", keysOf(seed.Files))
	}
	if _, ok := seed.Files["calc.sh"]; !ok {
		t.Fatal("calc.sh missing")
	}
}

func keysOf(m map[string][]byte) string {
	var b strings.Builder
	for k := range m {
		b.WriteString(k + " ")
	}
	return b.String()
}

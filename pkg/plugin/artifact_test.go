package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// TestShippedHelloPluginIsTheTestedArtifact pins the only plugin a customer can
// actually run to the one this package executes in its tests.
//
// examples/hello-plugin/hello.wasm is what a release hands someone who wants to
// try the product. pkg/plugin/testdata/hello.wasm is what the execution tests
// run through a real WASM runtime on every CI run. They are two committed copies
// of one build, and without this test nothing keeps them in step. Once they
// drift the suite stays green while the customer runs a module nothing has ever
// executed — the failure mode where coverage exists and means nothing.
//
// Byte equality is the invariant, not "rebuild and compare": a compiled module
// embeds the toolchain that produced it, so any Go version bump changes every
// byte. Rebuilding on demand (as internal/core/sandbox does for its own
// fixtures) would remove the committed copy entirely; until then, equality is
// what keeps the shipped artifact honest.
func TestShippedHelloPluginIsTheTestedArtifact(t *testing.T) {
	const wasmMagic = "\x00asm"

	shipped := readHelloWasm(t, filepath.Join("..", "..", "examples", "hello-plugin", "hello.wasm"))
	tested := readHelloWasm(t, filepath.Join("testdata", "hello.wasm"))

	for name, data := range map[string][]byte{"shipped": shipped, "tested": tested} {
		if len(data) < 8 || string(data[:4]) != wasmMagic {
			t.Fatalf("the %s copy is not a wasm module (%d bytes); it was committed corrupted", name, len(data))
		}
	}

	if !bytes.Equal(shipped, tested) {
		t.Fatalf("the example plugin a customer runs has drifted from the one these tests execute:\n"+
			"  examples/hello-plugin/hello.wasm  %s (%d bytes)\n"+
			"  pkg/plugin/testdata/hello.wasm     %s (%d bytes)\n"+
			"rebuild one of them (GOOS=wasip1 GOARCH=wasm go build ./examples/hello-plugin/) and copy it to both paths",
			sha256Hex(shipped), len(shipped), sha256Hex(tested), len(tested))
	}
}

func readHelloWasm(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

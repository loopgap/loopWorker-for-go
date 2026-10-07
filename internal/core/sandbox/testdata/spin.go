// Command spin is a hostile-by-design WASI module: it never returns.
//
// It exists so internal/core/sandbox can prove that a module produced by a real
// toolchain — real Go runtime, real WASI imports, megabytes of linear memory —
// is still killed by the CPU budget instead of running forever. The synthetic
// fixtures the other tests assemble byte by byte do not exercise the real
// code shape, which is where a fuel/interrupt accounting bug would hide.
//
// It is compiled on demand by the tests (see realSpinArtifact) and deliberately
// not committed as a .wasm binary: a prebuilt artifact cannot be kept in sync
// with the source that produced it, and a stale one would quietly stop testing
// what it claims to test.
//
// Build it by hand with:
//
//	GOOS=wasip1 GOARCH=wasm go build -o spin.wasm ./testdata/spin.go
package main

import (
	"fmt"
	"os"
)

// sink keeps the loop observable so the compiler cannot prove it is dead code
// and elide it. It is a package-level variable for exactly that reason.
var sink uint64

func main() {
	fmt.Fprintln(os.Stderr, "spin: started; this module never finishes on its own")

	for i := uint64(0); ; i++ {
		// Burn CPU with real work rather than idling: the budget under test is
		// CPU seconds, so a sleeping loop would pass without testing anything.
		sink = sink*1664525 + 1013904223 + i

		// Speak occasionally so a human reading the plugin log can tell the
		// module was alive right up to the moment it was killed.
		if i%50_000_000 == 0 {
			fmt.Fprintf(os.Stderr, "spin: still running, %d iterations\n", i)
		}
	}
}

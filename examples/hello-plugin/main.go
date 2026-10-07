// Command hello is the smallest useful LoopWorker plugin: a WebAssembly command
// that echoes its input.
//
// It exists so the product can be verified end to end without a Rust or TinyGo
// toolchain: loopworker runs WASI commands, so this file is compiled with
//
//	GOOS=wasip1 GOARCH=wasm go build -o hello.wasm .
//
// The committed hello.wasm is a build of this source, so a customer who only
// downloaded a release still has something the sandbox can actually run. It is
// deliberately not required to match a fresh build: a compiled module embeds
// the toolchain that produced it, so every Go upgrade changes the bytes. What is
// enforced instead is that hello.wasm stays byte-identical to
// pkg/plugin/testdata/hello.wasm, which the test suite executes through a real
// WASM runtime — so what a customer runs is always what was tested. To
// regenerate both, build with the command above and copy the result to each
// path.
//
// Input arrives on stdin and output leaves on stdout — the WASI convention the
// sandbox relies on. There is no host function here on purpose: this plugin
// demonstrates the *baseline*, and a plugin that needs loopworker-specific
// imports is documented in docs/guides/.
package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	in, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hello: read stdin: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("hello from wasm: %s", in)
}

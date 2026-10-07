package sandbox

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Minimal hand-rolled wasm binary encoder, used only by the tests, so every
// fixture is a real .wasm artifact the production decoder and wazero both accept.

type blob struct{ b []byte }

func (w *blob) u8(v byte) { w.b = append(w.b, v) }

// uleb writes an unsigned LEB128 value.
func (w *blob) uleb(v uint32) {
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		w.u8(b)
		if v == 0 {
			return
		}
	}
}

// sleb writes a signed LEB128 i32 value (i32.const operands).
func (w *blob) sleb(v int32) {
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if (v == 0 && b&0x40 == 0) || (v == -1 && b&0x40 != 0) {
			w.u8(b)
			return
		}
		w.u8(b | 0x80)
	}
}

func (w *blob) name(s string) {
	w.uleb(uint32(len(s)))
	w.b = append(w.b, s...)
}

func (w *blob) raw(p []byte) { w.b = append(w.b, p...) }

// wasmSection frames a section body with its id and length.
func wasmSection(id byte, body []byte) []byte {
	var w blob
	w.u8(id)
	w.uleb(uint32(len(body)))
	w.raw(body)
	return w.b
}

// wasmModule concatenates the preamble and the given sections.
func wasmModule(sections ...[]byte) []byte {
	var w blob
	w.raw([]byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00})
	for _, s := range sections {
		w.raw(s)
	}
	return w.b
}

// funcType encodes a type section entry.
func funcType(params, results []byte) []byte {
	var w blob
	w.u8(0x60)
	w.uleb(uint32(len(params)))
	w.raw(params)
	w.uleb(uint32(len(results)))
	w.raw(results)
	return w.b
}

const (
	i32Type = 0x7f
	i64Type = 0x7e
)

func typeSection(types ...[]byte) []byte {
	var body blob
	body.uleb(uint32(len(types)))
	for _, t := range types {
		body.raw(t)
	}
	return wasmSection(1, body.b)
}

// importFuncSection declares a single imported function.
func importFuncSection(module, field string, typeIdx uint32) []byte {
	var body blob
	body.uleb(1)
	body.name(module)
	body.name(field)
	body.u8(0x00)
	body.uleb(typeIdx)
	return wasmSection(2, body.b)
}

func functionSection(typeIdxs ...uint32) []byte {
	var body blob
	body.uleb(uint32(len(typeIdxs)))
	for _, idx := range typeIdxs {
		body.uleb(idx)
	}
	return wasmSection(3, body.b)
}

// memorySection declares one memory with the given minimum pages and no maximum.
func memorySection(minPages uint32) []byte {
	var body blob
	body.uleb(1)
	body.u8(0x00)
	body.uleb(minPages)
	return wasmSection(5, body.b)
}

func exportFuncSection(name string, funcIdx uint32) []byte {
	var body blob
	body.uleb(1)
	body.name(name)
	body.u8(0x00)
	body.uleb(funcIdx)
	return wasmSection(7, body.b)
}

func exportMemorySection(name string) []byte {
	var body blob
	body.uleb(1)
	body.name(name)
	body.u8(0x02)
	body.uleb(0)
	return wasmSection(7, body.b)
}

func startSection(funcIdx uint32) []byte {
	var body blob
	body.uleb(funcIdx)
	return wasmSection(8, body.b)
}

// codeSection wraps function bodies; each body is locals-count(0) + instructions.
func codeSection(bodies ...[]byte) []byte {
	var body blob
	body.uleb(uint32(len(bodies)))
	for _, b := range bodies {
		var framed blob
		framed.u8(0x00) // no locals
		framed.raw(b)
		body.uleb(uint32(len(framed.b)))
		body.raw(framed.b)
	}
	return wasmSection(10, body.b)
}

// dataSection encodes active data segments: memory 0, absolute offset, payload.
func dataSection(segments ...[2]interface{}) []byte {
	var body blob
	body.uleb(uint32(len(segments)))
	for _, seg := range segments {
		body.uleb(0) // active, memory 0
		var expr blob
		expr.u8(0x41) // i32.const
		expr.sleb(seg[0].(int32))
		expr.u8(0x0b) // end
		body.raw(expr.b)
		data := seg[1].([]byte)
		body.uleb(uint32(len(data)))
		body.raw(data)
	}
	return wasmSection(11, body.b)
}

// fetchWasm returns a module whose start function calls env.host_http_request
// with url and traps if the host refuses or fails, so a plugin run succeeds only
// when the egress policy allows the request.
func fetchWasm(url string) []byte {
	startBody := []byte{}
	startBody = append(startBody, i32const(0)...)               // urlPtr
	startBody = append(startBody, i32const(int32(len(url)))...) // urlLen
	startBody = append(startBody, i32const(0)...)               // bodyPtr
	startBody = append(startBody, i32const(0)...)               // bodyLen
	startBody = append(startBody,
		0x10, 0x00, // call host_http_request
		0x50,       // i64.eqz
		0x04, 0x40, // if
		0x00, // unreachable
		0x0b, // end if
		0x0b, // end func
	)

	return wasmModule(
		typeSection(
			funcType([]byte{i32Type, i32Type, i32Type, i32Type}, []byte{i64Type}), // 0: host_http_request
			funcType([]byte{i32Type}, []byte{i32Type}),                            // 1: alloc
			funcType(nil, nil), // 2: start
		),
		importFuncSection(hostModuleEnv, "host_http_request", 0),
		functionSection(1, 2),
		memorySection(1),
		exportFuncSection("alloc", 1),
		startSection(2),
		codeSection(
			// alloc: return a fixed scratch pointer at offset 32.
			[]byte{0x41, 0x20, 0x0b},
			startBody,
		),
		dataSection([2]interface{}{int32(0), []byte(url)}),
	)
}

// i32const encodes an i32.const instruction with a signed operand.
func i32const(v int32) []byte {
	var w blob
	w.u8(0x41)
	w.sleb(v)
	return w.b
}

// growWasm returns a module that grows its memory by the given number of pages
// in its start function and traps when the growth is refused by the runtime.
func growWasm(pages uint32) []byte {
	return wasmModule(
		typeSection(funcType(nil, nil)),
		functionSection(0),
		memorySection(1),
		exportMemorySection("memory"),
		startSection(0),
		codeSection(append(
			i32const(int32(pages)),
			0x40, 0x00, // memory.grow 0
			0x41, 0x7f, // i32.const -1
			0x46,       // i32.eq
			0x04, 0x40, // if
			0x00,       // unreachable
			0x0b, 0x0b, // end if, end func
		)),
	)
}

// minMemoryWasm declares minPages at instantiation time and does nothing else.
func minMemoryWasm(minPages uint32) []byte {
	return wasmModule(
		typeSection(funcType(nil, nil)),
		functionSection(0),
		memorySection(minPages),
		startSection(0),
		codeSection([]byte{0x0b}),
	)
}

// spinWasm returns a module whose start function never returns.
func spinWasm() []byte {
	return wasmModule(
		typeSection(funcType(nil, nil)),
		functionSection(0),
		startSection(0),
		codeSection([]byte{0x03, 0x40, 0x0c, 0x00, 0x0b, 0x0b}),
	)
}

// noisyWasm returns a WASI module that writes payload bytes to stdout forever.
func noisyWasm(payload []byte) []byte {
	const iovecPtr = 0
	const nwrittenPtr = 8
	const payloadPtr = 16

	iovec := make([]byte, 8)
	binary.LittleEndian.PutUint32(iovec[0:], payloadPtr)
	binary.LittleEndian.PutUint32(iovec[4:], uint32(len(payload)))

	return wasmModule(
		typeSection(
			funcType([]byte{i32Type, i32Type, i32Type, i32Type}, []byte{i32Type}), // 0: fd_write
			funcType(nil, nil), // 1: start
		),
		importFuncSection(knownWASIModulePrefix, "fd_write", 0),
		functionSection(1),
		memorySection(2),
		startSection(1),
		codeSection([]byte{
			0x03, 0x40, // loop (void)
			0x41, 0x01, // fd = 1 (stdout)
			0x41, iovecPtr, // *iovs
			0x41, 0x01, // iovs_len
			0x41, nwrittenPtr, // res nwritten
			0x10, 0x00, // call fd_write
			0x1a,       // drop
			0x0c, 0x00, // br 0 (loop)
			0x0b, // end loop
			0x0b, // end func
		}),
		dataSection(
			[2]interface{}{int32(iovecPtr), iovec},
			[2]interface{}{int32(payloadPtr), payload},
		),
	)
}

// chatterWasm returns a WASI module that writes exactly payload to stdout once
// and returns. Unlike noisyWasm it is bounded, so a test can tell "the cap bit"
// apart from "the module eventually stopped": a payload between the plugin's cap
// and the sandbox's cap succeeds under one limit and fails under the other.
func chatterWasm(payload []byte) []byte {
	const iovecPtr = 0
	const nwrittenPtr = 8
	const payloadPtr = 16

	iovec := make([]byte, 8)
	binary.LittleEndian.PutUint32(iovec[0:], payloadPtr)
	binary.LittleEndian.PutUint32(iovec[4:], uint32(len(payload)))

	pages := uint32(payloadPtr+len(payload))/65536 + 1

	return wasmModule(
		typeSection(
			funcType([]byte{i32Type, i32Type, i32Type, i32Type}, []byte{i32Type}), // 0: fd_write
			funcType(nil, nil), // 1: start
		),
		importFuncSection(knownWASIModulePrefix, "fd_write", 0),
		functionSection(1),
		memorySection(pages),
		startSection(1),
		codeSection([]byte{
			0x41, 0x01, // fd = 1 (stdout)
			0x41, iovecPtr, // *iovs
			0x41, 0x01, // iovs_len
			0x41, nwrittenPtr, // res nwritten
			0x10, 0x00, // call fd_write
			0x1a, // drop
			0x0b, // end func
		}),
		dataSection(
			[2]interface{}{int32(iovecPtr), iovec},
			[2]interface{}{int32(payloadPtr), payload},
		),
	)
}

// unknownImportWasm imports a host module this sandbox does not provide.
func unknownImportWasm(module, function string) []byte {
	return wasmModule(
		typeSection(funcType(nil, nil), funcType(nil, nil)),
		importFuncSection(module, function, 0),
		functionSection(1),
		startSection(1),
		codeSection([]byte{0x0b}),
	)
}

// writeFixtureFile puts a real .wasm artifact on disk under testdata and returns
// its path.
func writeFixtureFile(t *testing.T, name string, data []byte) string {
	t.Helper()

	dir := filepath.Join("testdata")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir testdata: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	return path
}

// realEchoArtifact compiles the example plugin customers are told to use into a
// WASI module and returns the bytes.
//
// Reusing examples/hello-plugin rather than a fixture private to this package
// is deliberate: these tests then prove the module that ships in the
// documentation actually executes in the sandbox, instead of proving that some
// other, near-identical module does.
func realEchoArtifact(t *testing.T) []byte {
	t.Helper()
	return buildWasiModule(t, "./examples/hello-plugin")
}

// realSpinArtifact compiles the never-terminating module from testdata/spin.go.
// It is the one fixture that cannot reuse an existing package, because no
// in-tree module is hostile on purpose.
func realSpinArtifact(t *testing.T) []byte {
	t.Helper()
	return buildWasiModule(t, "./internal/core/sandbox/testdata/spin.go")
}

// buildWasiModule compiles arg with the real Go toolchain and returns the module
// bytes.
//
// Fixtures are compiled on demand rather than committed as .wasm binaries: a
// checked-in binary cannot be kept in sync with the source that produced it, and
// a stale one silently stops testing what it claims to test — which is worse than
// having no test, because the coverage still looks present.
//
// A build failure fails the test. It is never a skip. These three artifacts are
// the only ones in the package that execute a genuine WASI module end to end —
// real runtime, real imports, megabytes of linear memory — so a skip here would
// quietly remove the only coverage of the product's core promise. It used to
// skip, against skip messages pointing at source files that were never
// committed, so the tests could not have run in any clone of this repository.
func buildWasiModule(t *testing.T, arg string) []byte {
	t.Helper()

	out := filepath.Join(t.TempDir(), "artifact.wasm")
	cmd := exec.Command("go", "build", "-trimpath", "-o", out, arg)
	cmd.Dir = repoRoot(t)
	cmd.Env = wasiBuildEnv()
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s for wasip1: %v\n%s", arg, err, combined)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read %s: %v", out, err)
	}
	if len(data) < 8 || string(data[:4]) != string(wasmMagic) {
		t.Fatalf("building %s did not produce a wasm module", arg)
	}
	return data
}

// wasiBuildEnv is os.Environ() with GOOS/GOARCH/CGO_ENABLED replaced, so an
// inherited host setting cannot silently produce a native binary where a WASI
// module is required. Filtering first rather than appending matters: Windows
// passes duplicate environment entries through unreconciled.
func wasiBuildEnv() []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GOOS=") || strings.HasPrefix(kv, "GOARCH=") || strings.HasPrefix(kv, "CGO_ENABLED=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
}

// repoRoot walks up from the package directory to the module root so the fixture
// build does not depend on the caller's working directory.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod found above the package directory")
		}
		dir = parent
	}
}

// egressWasm returns a module whose start function asks the host for url and
// ignores the answer, so an audit sees the request without the module trapping.
func egressWasm(url string) []byte {
	startBody := []byte{}
	startBody = append(startBody, i32const(0)...)
	startBody = append(startBody, i32const(int32(len(url)))...)
	startBody = append(startBody, i32const(0)...)
	startBody = append(startBody, i32const(0)...)
	startBody = append(startBody, 0x10, 0x00, 0x1a, 0x0b) // call, drop, end

	return wasmModule(
		typeSection(
			funcType([]byte{i32Type, i32Type, i32Type, i32Type}, []byte{i64Type}),
			funcType(nil, nil),
		),
		importFuncSection(hostModuleEnv, "host_http_request", 0),
		functionSection(1),
		memorySection(1),
		startSection(1),
		codeSection(startBody),
		dataSection([2]interface{}{int32(0), []byte(url)}),
	)
}

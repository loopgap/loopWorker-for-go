package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"go.uber.org/zap"
	"loopworker/pkg/logger"
)

const (
	wasmPageSize = 65536
	// maxWasmPages is the 32-bit address space limit: 65536 * 64KiB = 4 GiB.
	maxWasmPages = 65536

	defaultHostHTTPTimeout = 15 * time.Second
)

// hostModuleEnv is the name of the module that exposes LoopWorker's host
// functions to wasm plugins.
const hostModuleEnv = "env"

// wasmEnvConfig configures the host functions a plugin is allowed to call.
type wasmEnvConfig struct {
	// AllowedHosts is the egress allowlist for host_http_request. An empty list
	// means no host may be reached at all.
	AllowedHosts []string
	// MaxResponseBytes bounds how much of an HTTP response is buffered in the
	// host before it is copied into wasm memory. Zero uses DefaultMaxOutputMB.
	MaxResponseBytes int64
	// EgressProbe, when non-nil, replaces the real HTTP call: the requested URL
	// is recorded and the call fails, so no traffic ever leaves the host. Used
	// by AuditWasm.
	EgressProbe func(requestURL string, body []byte)
	// HTTPClient overrides the default egress client (tests, proxies).
	HTTPClient *http.Client
	// LogSink overrides where host_log output goes. Nil sends it to the
	// application logger.
	LogSink func(moduleName, message string)
	// AllowAnyHost skips the egress allowlist check. Audit-only: combined with
	// EgressProbe nothing leaves the host, but every requested URL is recorded.
	AllowAnyHost bool
}

// memoryPages converts a MiB budget into wasm memory pages, rejecting budgets
// the 32-bit address space cannot express.
func memoryPages(maxMemoryMB int) (uint32, error) {
	if maxMemoryMB <= 0 {
		return 0, fmt.Errorf("%w: memory limit must be positive, got %d MiB", ErrLimitTooLarge, maxMemoryMB)
	}
	pages := int64(maxMemoryMB) * 1024 * 1024 / wasmPageSize
	if pages > maxWasmPages {
		return 0, fmt.Errorf("%w: %d MiB exceeds the 4 GiB wasm address space", ErrLimitTooLarge, maxMemoryMB)
	}
	if pages < 1 {
		pages = 1
	}
	return uint32(pages), nil
}

// newWasmRuntime builds a runtime owned by exactly one plugin. Memory limits
// are baked in when a module is compiled, so per-plugin limits require a
// per-plugin runtime; sharing one runtime would silently apply the first
// plugin's budget to every other plugin.
func newWasmRuntime(ctx context.Context, memoryPages uint32, env wasmEnvConfig) (wazero.Runtime, error) {
	r := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithMemoryLimitPages(memoryPages).
		WithCloseOnContextDone(true))

	if _, err := wasi_snapshot_preview1.Instantiate(ctx, r); err != nil {
		_ = r.Close(ctx)
		return nil, fmt.Errorf("instantiate wasi: %w", err)
	}

	envFunc := func(ctx context.Context, mod api.Module, urlPtr, urlLen, bodyPtr, bodyLen uint32) uint64 {
		return callHostHTTPRequest(ctx, mod, env, urlPtr, urlLen, bodyPtr, bodyLen)
	}
	logFunc := func(ctx context.Context, mod api.Module, msgPtr, msgLen uint32) {
		msg, ok := mod.Memory().Read(msgPtr, msgLen)
		if !ok {
			return
		}
		if env.LogSink != nil {
			env.LogSink(mod.Name(), string(msg))
			return
		}
		logger.Debug("wasm log", zap.String("module", mod.Name()), zap.String("message", string(msg)))
	}

	if _, err := r.NewHostModuleBuilder(hostModuleEnv).
		NewFunctionBuilder().
		WithFunc(envFunc).
		Export("host_http_request").
		NewFunctionBuilder().
		WithFunc(logFunc).
		Export("host_log").
		Instantiate(ctx); err != nil {
		_ = r.Close(ctx)
		return nil, fmt.Errorf("instantiate host module %q: %w", hostModuleEnv, err)
	}

	return r, nil
}

// callHostHTTPRequest implements env.host_http_request. It returns 0 on any
// failure; otherwise the packed (pointer<<32 | length) of the response copied
// into the module's memory through its "alloc" export.
func callHostHTTPRequest(ctx context.Context, mod api.Module, env wasmEnvConfig, urlPtr, urlLen, bodyPtr, bodyLen uint32) uint64 {
	mem := mod.Memory()

	urlBytes, ok := mem.Read(urlPtr, urlLen)
	if !ok {
		return 0
	}
	urlStr := string(urlBytes)

	var body []byte
	if bodyLen > 0 {
		if body, ok = mem.Read(bodyPtr, bodyLen); !ok {
			return 0
		}
	}

	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return 0
	}

	allowed := env.AllowAnyHost
	for _, host := range env.AllowedHosts {
		if parsedURL.Host == host {
			allowed = true
			break
		}
	}
	if !allowed {
		return 0
	}

	if env.EgressProbe != nil {
		env.EgressProbe(urlStr, body)
		return 0
	}

	var req *http.Request
	if len(body) > 0 {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
		if err != nil {
			return 0
		}
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
		if err != nil {
			return 0
		}
	}

	client := env.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultHostHTTPTimeout}
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()

	maxResponse := env.MaxResponseBytes
	if maxResponse <= 0 {
		maxResponse = int64(DefaultMaxOutputMB) * 1024 * 1024
	}
	// Read one byte past the cap: a longer response is rejected rather than
	// buffered, so a hostile endpoint cannot size the host's memory.
	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil || int64(len(respBytes)) > maxResponse {
		return 0
	}

	allocFunc := mod.ExportedFunction("alloc")
	if allocFunc == nil {
		return 0
	}
	results, err := allocFunc.Call(ctx, uint64(len(respBytes)))
	if err != nil || len(results) == 0 {
		return 0
	}
	resPtr := uint32(results[0])
	if len(respBytes) > 0 && !mem.Write(resPtr, respBytes) {
		return 0
	}

	return (uint64(resPtr) << 32) | uint64(len(respBytes))
}

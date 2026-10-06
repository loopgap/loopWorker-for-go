package sandbox

import (
	"errors"
	"fmt"

	"loopworker/pkg/utils"

	lwerrors "loopworker/pkg/errors"
)

// Typed failures raised by the loader and the auditor. They are wrapped with
// %w so callers can use errors.Is, and they are all non-retryable: the same
// artifact will always fail the same way.
var (
	ErrArtifactMissing  = errors.New("wasm artifact missing")
	ErrArtifactTooLarge = errors.New("wasm artifact exceeds size limit")
	ErrArtifactBadMagic = errors.New("file is not a wasm module")
	ErrChecksumMismatch = errors.New("wasm checksum mismatch")
	ErrUnsupportedABI   = errors.New("unsupported wasm ABI")
	ErrLimitTooLarge    = errors.New("requested limit exceeds host maximum")
	ErrManifestInvalid  = errors.New("invalid plugin manifest")
	ErrPluginUnloading  = errors.New("plugin is unloading")
)

// PluginPanicError reports that a plugin crashed during execution. It is
// deliberately distinct from a timeout: a panic is deterministic, so retrying
// it only burns the retry budget, and the operator must see "panicked" rather
// than "timeout".
type PluginPanicError struct {
	Plugin string
	Err    error // *utils.PanicError carrying the recovered value and stack
}

func (e *PluginPanicError) Error() string {
	return fmt.Sprintf("sandbox: plugin %q panicked: %v", e.Plugin, e.Err)
}

// Unwrap exposes both the sandbox-panic sentinel (for errors.Is classification
// and retry policy) and the underlying panic (for the message and stack).
func (e *PluginPanicError) Unwrap() []error {
	return []error{lwerrors.ErrSandboxPanic, e.Err}
}

// IsNonRetryable reports whether retrying the failure is futile because it is
// deterministic: the plugin panicked, or the artifact is malformed, oversized
// or violates a host-imposed limit. Self-heal must not spend its retry budget
// on these.
func IsNonRetryable(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, lwerrors.ErrSandboxPanic) ||
		errors.Is(err, ErrArtifactMissing) ||
		errors.Is(err, ErrArtifactTooLarge) ||
		errors.Is(err, ErrArtifactBadMagic) ||
		errors.Is(err, ErrChecksumMismatch) ||
		errors.Is(err, ErrUnsupportedABI) ||
		errors.Is(err, ErrLimitTooLarge) ||
		errors.Is(err, ErrManifestInvalid)
}

// panicError converts the outcome of a guarded goroutine into a
// PluginPanicError, or returns nil when the outcome was not a panic.
func panicError(pluginName string, err error) error {
	var pe *utils.PanicError
	if errors.As(err, &pe) {
		return &PluginPanicError{Plugin: pluginName, Err: pe}
	}
	return nil
}

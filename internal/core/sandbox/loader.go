package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ManifestFileName is the file that describes a wasm plugin inside its
// directory.
const ManifestFileName = "plugin.json"

// DefaultWasmEntry is the artifact name used when a manifest omits "entry".
const DefaultWasmEntry = "main.wasm"

// WasmManifest is the on-disk contract of a wasm plugin directory:
//
//	<root>/<name>/plugin.json   this manifest
//	<root>/<name>/<entry>       the .wasm artifact (entry defaults to main.wasm)
//
// Limits left at zero are taken from LoadOptions.Defaults. Fields the loader
// does not use (description, author, skills) are preserved so the same manifest
// can serve the plugin manager as well.
type WasmManifest struct {
	Name        string     `json:"name"`
	Version     string     `json:"version"`
	Description string     `json:"description"`
	Author      string     `json:"author"`
	Entry       string     `json:"entry"`
	SHA256      string     `json:"sha256"`
	Skills      []string   `json:"skills,omitempty"`
	Limits      WasmLimits `json:"limits"`
}

// LoadOptions governs what the loader will accept from disk. The zero value is
// usable and conservative; DefaultLoadOptions spells out the values.
type LoadOptions struct {
	// Defaults supplies limits for manifest fields left at zero.
	Defaults SandboxConfig
	// MaxArtifactBytes rejects an oversized artifact before reading it.
	MaxArtifactBytes int64
	// MaxMemoryMB is the host ceiling: a manifest asking for more is refused
	// with ErrLimitTooLarge instead of being honoured.
	MaxMemoryMB int
	// RequireChecksum makes a manifest sha256 mandatory.
	RequireChecksum bool
}

// DefaultLoadOptions returns the conservative defaults: 128 MiB artifacts and a
// 256 MiB per-plugin memory ceiling.
func DefaultLoadOptions() LoadOptions {
	return LoadOptions{
		Defaults:         SandboxConfig{},
		MaxArtifactBytes: DefaultMaxArtifactMB * 1024 * 1024,
		MaxMemoryMB:      DefaultMaxMemoryMB,
	}
}

func (o LoadOptions) withDefaults() LoadOptions {
	if o.MaxArtifactBytes <= 0 {
		o.MaxArtifactBytes = DefaultMaxArtifactMB * 1024 * 1024
	}
	if o.MaxMemoryMB <= 0 {
		o.MaxMemoryMB = DefaultMaxMemoryMB
	}
	defaults, err := o.Defaults.withDefaults()
	if err == nil {
		o.Defaults = defaults
	}
	return o
}

// LoadWasmDir verifies one plugin directory and registers it in the sandbox.
//
// Verification, in order: manifest parses; artifact path stays inside the
// plugin directory (no path traversal); artifact size is within
// MaxArtifactBytes; the wasm magic preamble is present; the digest matches the
// manifest; the module's declared imports and entrypoint
// are ones this host supports; its memory request fits the per-plugin limit.
//
// Every rejection is a typed error: ErrArtifactMissing, ErrArtifactTooLarge,
// ErrArtifactBadMagic, ErrChecksumMismatch, ErrUnsupportedABI, ErrLimitTooLarge
// or ErrManifestInvalid.
func (s *Sandbox) LoadWasmDir(ctx context.Context, dir string, opts LoadOptions) (*WasmPlugin, error) {
	manifest, data, limits, err := verifyWasmDir(dir, s.resolveLoadOptions(opts))
	if err != nil {
		return nil, err
	}

	plugin, err := NewWasmPlugin(ctx, WasmPluginConfig{
		Name:    manifest.Name,
		Version: manifest.Version,
		Wasm:    data,
		Limits:  limits,
	})
	if err != nil {
		return nil, err
	}

	if err := s.LoadPlugin(manifest.Name, plugin); err != nil {
		// Never leak the runtime of a plugin that could not be registered.
		_ = plugin.Close(ctx)
		return nil, err
	}
	return plugin, nil
}

// LoadWasmTree loads every plugin directory beneath root, in name order. It
// stops at the first failure and returns the names loaded before it.
func (s *Sandbox) LoadWasmTree(ctx context.Context, root string, opts LoadOptions) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrArtifactMissing, root)
		}
		return nil, fmt.Errorf("read plugin root %s: %w", root, err)
	}

	var loaded []string
	resolved := s.resolveLoadOptions(opts)

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if _, err := os.Stat(filepath.Join(dir, ManifestFileName)); errors.Is(err, os.ErrNotExist) {
			continue // not a plugin directory
		}
		if _, err := s.LoadWasmDir(ctx, dir, resolved); err != nil {
			return loaded, fmt.Errorf("load %s: %w", dir, err)
		}
		loaded = append(loaded, entry.Name())
	}
	sort.Strings(loaded)
	return loaded, nil
}

// resolveLoadOptions lets an unset LoadOptions.Defaults inherit this sandbox's
// configuration, so a manifest that omits a limit gets the budget the sandbox
// was created with instead of an unrelated global default.
func (s *Sandbox) resolveLoadOptions(opts LoadOptions) LoadOptions {
	if opts.Defaults.isZero() {
		opts.Defaults = s.config
	}
	return opts.withDefaults()
}

// verifyWasmDir runs every artifact check and returns the manifest, the
// artifact bytes and the resolved limits.
func verifyWasmDir(dir string, opts LoadOptions) (*WasmManifest, []byte, WasmLimits, error) {
	o := opts.withDefaults()

	manifest, err := readWasmManifest(dir)
	if err != nil {
		return nil, nil, WasmLimits{}, err
	}

	artifactPath, err := resolveEntryPath(dir, manifest.Entry)
	if err != nil {
		return nil, nil, WasmLimits{}, err
	}

	data, err := readArtifact(artifactPath, o.MaxArtifactBytes)
	if err != nil {
		return nil, nil, WasmLimits{}, err
	}

	if err := checkMagic(data); err != nil {
		return nil, nil, WasmLimits{}, fmt.Errorf("%w: %s", err, artifactPath)
	}

	if err := checkChecksums(manifest, data, o); err != nil {
		return nil, nil, WasmLimits{}, err
	}

	limits := resolveLimits(manifest.Limits, o)
	if limits.MemoryMB > o.MaxMemoryMB {
		return nil, nil, WasmLimits{}, fmt.Errorf("%w: plugin %q asks for %d MiB, host ceiling is %d MiB",
			ErrLimitTooLarge, manifest.Name, limits.MemoryMB, o.MaxMemoryMB)
	}

	sum, err := inspect(data)
	if err != nil {
		return nil, nil, WasmLimits{}, err
	}
	if !sum.magicOK {
		return nil, nil, WasmLimits{}, fmt.Errorf("%w: %s", ErrArtifactBadMagic, artifactPath)
	}
	if err := checkABI(manifest.Name, sum, limits); err != nil {
		return nil, nil, WasmLimits{}, err
	}

	return manifest, data, limits, nil
}

// readWasmManifest loads and normalises <dir>/plugin.json.
func readWasmManifest(dir string) (*WasmManifest, error) {
	path := filepath.Join(dir, ManifestFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrManifestInvalid, path)
		}
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}

	var manifest WasmManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		// %w on both: the retry classifier asks errors.Is about the decode
		// failure itself, not only about the manifest verdict wrapping it.
		return nil, fmt.Errorf("%w: %s: %w", ErrManifestInvalid, path, err)
	}

	if manifest.Name == "" {
		manifest.Name = filepath.Base(dir)
	}
	if manifest.Entry == "" {
		manifest.Entry = DefaultWasmEntry
	}
	if strings.ContainsAny(manifest.Name, `/\`) || manifest.Name == "." || manifest.Name == ".." {
		return nil, fmt.Errorf("%w: plugin name %q must be a single path element", ErrManifestInvalid, manifest.Name)
	}
	return &manifest, nil
}

// resolveEntryPath keeps the manifest's entry inside the plugin directory.
func resolveEntryPath(dir, entry string) (string, error) {
	if filepath.IsAbs(entry) {
		return "", fmt.Errorf("%w: entry %q must be relative to the plugin directory", ErrManifestInvalid, entry)
	}
	candidate := filepath.Clean(filepath.Join(dir, entry))
	root := filepath.Clean(dir) + string(os.PathSeparator)
	if !strings.HasPrefix(candidate+string(os.PathSeparator), root) {
		return "", fmt.Errorf("%w: entry %q escapes the plugin directory", ErrManifestInvalid, entry)
	}
	return candidate, nil
}

// checkMagic reports whether the artifact begins with the wasm preamble.
func checkMagic(data []byte) error {
	if len(data) < 8 || string(data[:4]) != string(wasmMagic) {
		return ErrArtifactBadMagic
	}
	return nil
}

// VerifyArtifactDigest checks one artifact against the digest its manifest
// declares. It is the exported door to checkChecksums for callers that already
// hold the artifact bytes - pkg/plugin does, because it compiles the module
// itself instead of going through LoadWasmDir - so there is exactly one
// implementation of "what a digest mismatch means".
//
// declared may be empty, in which case require decides: false accepts a manifest
// that claims nothing (the historical behaviour), true refuses it, which is
// what an operator asking for verification actually wants. Either way a refusal
// wraps ErrChecksumMismatch.
func VerifyArtifactDigest(name, declared string, data []byte, require bool) error {
	declared = strings.TrimSpace(declared)
	err := checkChecksums(&WasmManifest{Name: name, SHA256: declared}, data, LoadOptions{RequireChecksum: require})
	if err == nil {
		return nil
	}
	if declared == "" {
		return fmt.Errorf("%w: plugin %q declares no sha256 and checksum verification is on", err, name)
	}
	sum := sha256.Sum256(data)
	return fmt.Errorf("%w: plugin.json declares sha256 %s, the artifact hashes to %s",
		err, declared, hex.EncodeToString(sum[:]))
}

func checkChecksums(manifest *WasmManifest, data []byte, opts LoadOptions) error {
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])

	if manifest.SHA256 == "" {
		if opts.RequireChecksum {
			return fmt.Errorf("%w: plugin %q declares no sha256 and the loader requires one", ErrChecksumMismatch, manifest.Name)
		}
		return nil
	}
	if !matchesSHA256(manifest.SHA256, sum[:]) {
		return fmt.Errorf("%w: plugin %q digest %s does not match manifest", ErrChecksumMismatch, manifest.Name, digest)
	}
	return nil
}

// resolveLimits overlays manifest limits on the loader defaults.
//
// This path is deliberately the strict one and does NOT clamp, the way the
// runtime's resolveWasmLimits does: verifyWasmDir refuses a manifest that asks
// for more memory than LoadOptions.MaxMemoryMB, so an over-limit plugin fails to
// load loudly instead of being silently reduced. Keep the two apart
// deliberately - and if one is ever taught to clamp, teach both, or a manifest
// will mean different things depending on which loader read it.
func resolveLimits(limits WasmLimits, opts LoadOptions) WasmLimits {
	resolved := limits
	if resolved.MemoryMB == 0 {
		resolved.MemoryMB = opts.Defaults.MaxMemoryMB
	}
	if resolved.MaxCPUSeconds == 0 {
		resolved.MaxCPUSeconds = opts.Defaults.MaxCPUSeconds
	}
	if resolved.MaxOutputMB == 0 {
		resolved.MaxOutputMB = opts.Defaults.MaxOutputMB
	}
	if resolved.AllowedHosts == nil {
		resolved.AllowedHosts = opts.Defaults.AllowedHosts
	}
	return resolved
}

// checkABI rejects modules the host cannot honourably run: unknown imports
// (which would either fail instantiation or be granted access we never
// intended) and a memory demand above the plugin's own budget.
func checkABI(name string, sum *staticSummary, limits WasmLimits) error {
	known := map[string]bool{"env": true, knownWASIModulePrefix: true}

	for _, imp := range sum.imports {
		if !known[imp.Module] {
			return fmt.Errorf("%w: plugin %q imports from unknown module %q", ErrUnsupportedABI, name, imp.Module)
		}
		if imp.Module == "env" && imp.Kind == "func" && !knownEnvFunctions[imp.Name] {
			return fmt.Errorf("%w: plugin %q imports unknown host function env:%s", ErrUnsupportedABI, name, imp.Name)
		}
	}

	if !sum.hasEntry {
		return fmt.Errorf("%w: plugin %q exports no _start entrypoint", ErrUnsupportedABI, name)
	}

	if int64(sum.minPages)*wasmPageSize > int64(limits.MemoryMB)*1024*1024 {
		return fmt.Errorf("%w: plugin %q requires %d memory pages (%d MiB), limit is %d MiB",
			ErrLimitTooLarge, name, sum.minPages,
			int64(sum.minPages)*wasmPageSize/(1<<20), limits.MemoryMB)
	}
	return nil
}

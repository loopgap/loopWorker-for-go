package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// Environment variables interpreted by the loader itself.
const (
	// EnvConfig points at the configuration file, equivalent to --config.
	EnvConfig = "LOOPWORKER_CONFIG"
	// EnvWorkDir sets the base directory that data.dir and plugins.dir derive from.
	EnvWorkDir = "LOOPWORKER_WORK_DIR"
)

// Options drives Load. Flags carries only the flags the user actually set,
// keyed by canonical config path (for example "server.port").
type Options struct {
	ConfigFile string
	Flags      map[string]string
}

// candidateFileNames are searched, in order, when no config file is given.
var candidateFileNames = []string{"config.yaml", "config.yml", "config.json"}

// Load resolves configuration through the fixed precedence
// defaults < config file < environment < flags, then validates the result.
// It fails on an unknown key, an unparsable value or an invalid combination.
func Load(opts Options) (*Config, error) {
	baseDir, err := os.Getwd()
	if err != nil {
		baseDir = "."
	}

	cfg := Defaults()

	path, searched, err := resolveConfigFile(opts.ConfigFile)
	if err != nil {
		return nil, err
	}
	if path != "" {
		if err := applyFile(cfg, path); err != nil {
			return nil, err
		}
		cfg.configFile = path
	} else {
		cfg.searchedPaths = searched
	}

	if err := applyEnv(cfg); err != nil {
		return nil, err
	}

	if err := applyFlags(cfg, opts.Flags); err != nil {
		return nil, err
	}

	if err := cfg.finalize(baseDir); err != nil {
		return nil, err
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func resolveConfigFile(explicit string) (string, []string, error) {
	if explicit == "" {
		explicit = os.Getenv(EnvConfig)
	}
	if explicit != "" {
		abs, err := filepath.Abs(explicit)
		if err != nil {
			return "", nil, fmt.Errorf("resolve config path %s: %w", explicit, err)
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			if os.IsNotExist(err) {
				return "", nil, fmt.Errorf("config file %s does not exist: pass an existing file with --config, unset %s, or omit both to run on defaults", abs, EnvConfig)
			}
			return "", nil, fmt.Errorf("read config file %s: %w", abs, err)
		}
		if len(data) == 0 {
			return "", nil, fmt.Errorf("config file %s is empty", abs)
		}
		return abs, nil, nil
	}

	dirs, err := searchDirs()
	if err != nil {
		return "", nil, err
	}
	for _, dir := range dirs {
		for _, name := range candidateFileNames {
			candidate := filepath.Join(dir, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Size() > 0 {
				return candidate, nil, nil
			}
		}
	}
	// Nothing was found, so the caller runs on defaults. Return the directories
	// anyway: "I edited config.yaml and the setting did not change" is
	// unanswerable without knowing where the loader looked, and a file left in
	// the wrong directory produces no other clue at all.
	return "", dirs, nil
}

func searchDirs() ([]string, error) {
	dirs := []string{"."}
	home, err := os.UserHomeDir()
	if err == nil {
		dirs = append(dirs, filepath.Join(home, ".loopworker"))
	}
	if wd := os.Getenv(EnvWorkDir); wd != "" {
		dirs = append(dirs, wd)
	}
	if runtimeRoot := os.Getenv("LOOPWORKER_ETC_DIR"); runtimeRoot != "" {
		dirs = append(dirs, runtimeRoot)
	} else {
		dirs = append(dirs, "/etc/loopworker")
	}
	return dirs, nil
}

func applyFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config file %s: %w", path, err)
	}

	format, err := detectFormat(path, data)
	if err != nil {
		return err
	}

	var raw map[string]interface{}
	switch format {
	case "json":
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("parse config file %s as JSON: %w", path, err)
		}
	case "yaml":
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("parse config file %s as YAML: %w", path, err)
		}
	}

	flat := map[string]interface{}{}
	if err := flatten("", raw, flat); err != nil {
		return fmt.Errorf("config file %s: %w", path, err)
	}

	source := "file:" + path
	seen := map[string]string{}
	keys := make([]string, 0, len(flat))
	for k := range flat {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		sp, matched, ok := specByKey(key)
		if !ok {
			return unknownKeyError(key, path)
		}
		if other, dup := seen[sp.path]; dup {
			return fmt.Errorf("config file %s: %q and %q both set %q; keep one", path, other, matched, sp.path)
		}
		seen[sp.path] = matched

		value, err := stringify(sp, flat[key])
		if err != nil {
			return fmt.Errorf("config file %s: %s: %w", path, key, err)
		}
		if err := applySpec(cfg, sp, value, source); err != nil {
			return err
		}
	}
	return nil
}

func unknownKeyError(key, path string) error {
	got := normalizeKey(key)
	var close []string
	for _, candidate := range acceptedKeys() {
		if near(got, normalizeKey(candidate)) {
			close = append(close, candidate)
		}
	}
	hint := ""
	if len(close) > 0 {
		hint = fmt.Sprintf(" (did you mean %s?)", strings.Join(close, ", "))
	}
	return fmt.Errorf("unknown configuration key %q in %s%s\n  accepted keys: %s",
		key, path, hint, strings.Join(acceptedKeys(), ", "))
}

func applyEnv(cfg *Config) error {
	for _, sp := range specs() {
		for _, name := range sp.env {
			value, ok := os.LookupEnv(name)
			if !ok {
				continue
			}
			if err := applySpec(cfg, sp, value, "env:"+name); err != nil {
				return err
			}
			break
		}
	}
	return nil
}

func applyFlags(cfg *Config, flags map[string]string) error {
	names := make([]string, 0, len(flags))
	for name := range flags {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		sp, ok := specByPath(name)
		if !ok {
			return fmt.Errorf("internal: flag %q is not a known configuration key", name)
		}
		if err := applySpec(cfg, sp, flags[name], "flag:--"+strings.ReplaceAll(sp.path, ".", "-")); err != nil {
			return err
		}
	}
	return nil
}

func applySpec(cfg *Config, sp spec, value, source string) error {
	if err := sp.set(cfg, value); err != nil {
		return fmt.Errorf("%s: %w (source: %s)", sp.path, err, source)
	}
	cfg.sources[sp.path] = source
	return nil
}

// finalize expands ~ and relative paths, and derives data.dir / plugins.dir from
// work_dir when the user did not set them explicitly.
func (c *Config) finalize(baseDir string) error {
	if c.WorkDir != "" {
		abs, err := absolutize(baseDir, c.WorkDir)
		if err != nil {
			return fmt.Errorf("work_dir: %w (source: %s)", err, c.SourceOf("work_dir"))
		}
		c.WorkDir = abs
	}

	derived := map[string]string{"data.dir": "data", "plugins.dir": "plugins"}
	for path, sub := range derived {
		switch {
		case c.WorkDir != "" && c.SourceOf(path) == "default":
			c.setField(path, filepath.Join(c.WorkDir, sub))
		case c.getField(path) != "":
			abs, err := absolutize(baseDir, c.getField(path))
			if err != nil {
				return fmt.Errorf("%s: %w (source: %s)", path, err, c.SourceOf(path))
			}
			c.setField(path, abs)
		default:
			return fmt.Errorf("%s: cannot be empty and has no work_dir to derive from; set %s or --work-dir", path, EnvWorkDir)
		}
	}

	// data.db_file stays relative to data.dir unless an absolute path is given.
	expanded, err := expandHome(c.Data.DBFile)
	if err != nil {
		return fmt.Errorf("data.db_file: %w (source: %s)", err, c.SourceOf("data.db_file"))
	}
	c.Data.DBFile = expanded

	if c.WorkDir == "" {
		c.WorkDir = filepath.Dir(c.Data.Dir)
	}
	return nil
}

func (c *Config) setField(path, value string) {
	if sp, ok := specByPath(path); ok {
		_ = sp.set(c, value)
	}
}

func (c *Config) getField(path string) string {
	if sp, ok := specByPath(path); ok {
		return sp.get(c)
	}
	return ""
}

func absolutize(baseDir, path string) (string, error) {
	expanded, err := expandHome(path)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(expanded) {
		return filepath.Clean(expanded), nil
	}
	return filepath.Clean(filepath.Join(baseDir, expanded)), nil
}

func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot expand ~ in %q because the home directory is unknown; use an absolute path or set %s", path, EnvWorkDir)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

func detectFormat(path string, data []byte) (string, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return "json", nil
	case ".yaml", ".yml":
		return "yaml", nil
	}
	if json.Valid(data) {
		return "json", nil
	}
	if len(data) > 0 && (data[0] == '{' || data[0] == '[') {
		return "", fmt.Errorf("config file %s looks like JSON but does not parse; name it *.json or fix the syntax", path)
	}
	return "yaml", nil
}

func flatten(prefix string, in map[string]interface{}, out map[string]interface{}) error {
	for k, v := range in {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if child, ok := v.(map[string]interface{}); ok {
			if err := flatten(key, child, out); err != nil {
				return err
			}
			continue
		}
		out[key] = v
	}
	return nil
}

func stringify(sp spec, raw interface{}) (string, error) {
	switch v := raw.(type) {
	case nil:
		return "", fmt.Errorf("expected a value, got null")
	case string:
		return v, nil
	case bool:
		return strconv.FormatBool(v), nil
	case int:
		return strconv.Itoa(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case float64:
		if v != float64(int64(v)) {
			return "", fmt.Errorf("expected a whole number, got %v", v)
		}
		return strconv.FormatInt(int64(v), 10), nil
	case []interface{}:
		if !strings.HasSuffix(sp.path, "_hosts") {
			return "", fmt.Errorf("expected a %s, got a list", describeKind(sp.path))
		}
		parts := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return "", fmt.Errorf("expected a list of strings, got %v inside the list", item)
			}
			parts = append(parts, s)
		}
		return strings.Join(parts, ","), nil
	case map[string]interface{}:
		return "", fmt.Errorf("expected a %s, got a nested block", describeKind(sp.path))
	default:
		return fmt.Sprintf("%v", v), nil
	}
}

func describeKind(path string) string {
	switch {
	case strings.HasSuffix(path, "_timeout"), strings.HasSuffix(path, "_backoff"):
		return "duration such as 30s or 5m"
	case strings.HasSuffix(path, "_port"), strings.HasSuffix(path, "_count"),
		strings.HasSuffix(path, "_mb"), strings.HasSuffix(path, "_seconds"),
		strings.HasSuffix(path, "_threshold"), strings.HasSuffix(path, "_attempts"),
		strings.HasSuffix(path, "max_concurrent"):
		return "integer"
	case strings.HasSuffix(path, "_load"), strings.HasSuffix(path, "_enabled"), strings.HasSuffix(path, "_required"),
		strings.HasSuffix(path, "_checksum"):
		return "true or false"
	default:
		return "string"
	}
}

// near reports whether two key paths are similar enough to suggest one.
func near(got, candidate string) bool {
	if got == candidate || strings.Contains(candidate, got) || strings.Contains(got, candidate) {
		return true
	}
	return editDistanceWithin(got, candidate, 2)
}

func editDistanceWithin(a, b string, max int) bool {
	la, lb := len(a), len(b)
	if abs(la-lb) > max {
		return false
	}
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur[0] = i
		worst := i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if cur[j] < worst {
				worst = cur[j]
			}
		}
		if worst > max {
			return false
		}
		prev, cur = cur, prev
	}
	return prev[lb] <= max
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
